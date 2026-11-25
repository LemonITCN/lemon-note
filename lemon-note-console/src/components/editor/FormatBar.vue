<script setup lang="ts">
// 选中文字时的格式工具条。
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Editor } from '@tiptap/core'
import { BubbleMenu } from '@tiptap/vue-3/menus'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import { COLORS } from './extensions/marks'
import { addToChat } from '@/lib/chatRefs'

const props = defineProps<{ editor: Editor; pageId: string; pageTitle: string }>()
const { t } = useI18n()
const linkOpen = ref(false)
const linkDraft = ref('')
const e = computed(() => props.editor)

const turnInto = computed(() => [
  { label: t('editor.text'), icon: 'text', run: () => e.value.chain().focus().setParagraph().run() },
  { label: t('editor.h1'), icon: 'h1', run: () => e.value.chain().focus().setHeading({ level: 1 }).run() },
  { label: t('editor.h2'), icon: 'h2', run: () => e.value.chain().focus().setHeading({ level: 2 }).run() },
  { label: t('editor.h3'), icon: 'h3', run: () => e.value.chain().focus().setHeading({ level: 3 }).run() },
  { label: t('editor.bullet'), icon: 'list-bullet', run: () => e.value.chain().focus().toggleBulletList().run() },
  { label: t('editor.numbered'), icon: 'list-number', run: () => e.value.chain().focus().toggleOrderedList().run() },
  { label: t('editor.todo'), icon: 'list-check', run: () => e.value.chain().focus().toggleTaskList().run() },
  { label: t('editor.quote'), icon: 'quote', run: () => e.value.chain().focus().toggleBlockquote().run() },
  { label: t('editor.code'), icon: 'code', run: () => e.value.chain().focus().toggleCodeBlock().run() },
])

function shouldShow({ editor, state }: any) {
  const { selection } = state
  if (selection.empty || !editor.isEditable) return false
  if (selection.node) return false
  const $f = selection.$from
  if ($f.parent.type.name === 'codeBlock') return false
  return true
}

function openLink() {
  linkDraft.value = e.value.getAttributes('link').href || ''
  linkOpen.value = true
}

function applyLink() {
  const v = linkDraft.value.trim()
  if (!v) e.value.chain().focus().extendMarkRange('link').unsetLink().run()
  else {
    const href = /^[a-z]+:/i.test(v) || v.startsWith('/') ? v : 'https://' + v
    if (/^javascript:/i.test(href)) return
    e.value.chain().focus().extendMarkRange('link').setLink({ href }).run()
  }
  linkOpen.value = false
}

function comment() {
  const { state } = props.editor
  const { from, to } = state.selection
  if (from === to) return
  const top = state.doc.resolve(from).node(1)
  props.editor.emit('lnComment' as any, { from, to, quote: state.doc.textBetween(from, to, ' '), blockId: top?.attrs?.id || '' })
}

function chat() {
  const { from, to } = e.value.state.selection
  const text = e.value.state.doc.textBetween(from, to, '\n')
  const ids: string[] = []
  e.value.state.doc.nodesBetween(from, to, (n, _pos, parent) => { if (parent === e.value.state.doc && n.attrs.id) ids.push(n.attrs.id) })
  addToChat({ kind: 'selection', id: ids[0] || '', pageId: props.pageId, blockIds: ids, text: text.slice(0, 4000), title: props.pageTitle })
}
</script>

<template>
  <BubbleMenu :editor="editor" :should-show="shouldShow" :options="{ placement: 'top', offset: 8 }" class="fb">
    <button class="fb-btn chat" @mousedown.prevent @click="chat"><Icon name="assistant" :size="15" />{{ t('editor.addToChat') }}</button>
    <button class="fb-icon" :title="t('comments.add')" @mousedown.prevent @click="comment"><Icon name="comment" :size="16" /></button>
    <span class="fb-sep" />
    <Menu :items="turnInto"><button class="fb-btn" @mousedown.prevent>{{ t('editor.turnInto') }}<Icon name="chevron-down" :size="13" /></button></Menu>
    <span class="fb-sep" />
    <button class="fb-icon" :class="{ on: editor.isActive('bold') }" :title="t('editor.bold')" @mousedown.prevent @click="editor.chain().focus().toggleBold().run()"><Icon name="bold" :size="16" /></button>
    <button class="fb-icon" :class="{ on: editor.isActive('italic') }" :title="t('editor.italic')" @mousedown.prevent @click="editor.chain().focus().toggleItalic().run()"><Icon name="italic" :size="16" /></button>
    <button class="fb-icon" :class="{ on: editor.isActive('underline') }" :title="t('editor.underline')" @mousedown.prevent @click="editor.chain().focus().toggleUnderline().run()"><Icon name="underline" :size="16" /></button>
    <button class="fb-icon" :class="{ on: editor.isActive('strike') }" :title="t('editor.strike')" @mousedown.prevent @click="editor.chain().focus().toggleStrike().run()"><Icon name="strike" :size="16" /></button>
    <button class="fb-icon" :class="{ on: editor.isActive('code') }" :title="t('editor.inlineCode')" @mousedown.prevent @click="editor.chain().focus().toggleCode().run()"><Icon name="inline-code" :size="16" /></button>
    <button class="fb-icon" :title="t('editor.inlineMath')" @mousedown.prevent @click="editor.chain().focus().insertContent({ type: 'mathInline', attrs: { latex: editor.state.doc.textBetween(editor.state.selection.from, editor.state.selection.to) } }).run()"><Icon name="math" :size="16" /></button>
    <PopoverRoot v-model:open="linkOpen">
      <PopoverTrigger as-child><button class="fb-icon" :class="{ on: editor.isActive('link') }" :title="t('editor.link')" @mousedown.prevent @click="openLink"><Icon name="link" :size="16" /></button></PopoverTrigger>
      <PopoverPortal>
        <PopoverContent class="fb-link" side="bottom" :side-offset="8" @open-auto-focus.prevent>
          <input v-model="linkDraft" :placeholder="t('editor.linkPlaceholder')" autofocus @keydown.enter.prevent="applyLink" />
          <button @click="applyLink">{{ t('common.apply') }}</button>
        </PopoverContent>
      </PopoverPortal>
    </PopoverRoot>
    <PopoverRoot>
      <PopoverTrigger as-child><button class="fb-icon" :title="t('editor.color')" @mousedown.prevent><span class="fb-a">A</span></button></PopoverTrigger>
      <PopoverPortal>
        <PopoverContent class="fb-colors" side="bottom" :side-offset="8">
          <div class="fb-ctitle">{{ t('editor.textColor') }}</div>
          <div class="fb-cgrid">
            <button class="fb-c" @click="editor.chain().focus().unsetTextColor().run()">A</button>
            <button v-for="c in COLORS" :key="c" class="fb-c" :class="'c-' + c" :title="t('colors.' + c)" @click="editor.chain().focus().setTextColor(c).run()">A</button>
          </div>
          <div class="fb-ctitle">{{ t('editor.background') }}</div>
          <div class="fb-cgrid">
            <button class="fb-c" @click="editor.chain().focus().unsetHighlight().run()">A</button>
            <button v-for="c in COLORS" :key="c" class="fb-c" :class="'cb-' + c" :title="t('colors.' + c)" @click="editor.chain().focus().setHighlight(c).run()">A</button>
          </div>
        </PopoverContent>
      </PopoverPortal>
    </PopoverRoot>
  </BubbleMenu>
</template>

<style>
.fb { display: flex; align-items: center; gap: 1px; background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 3px; z-index: 50; }
.fb-btn { display: inline-flex; align-items: center; gap: 4px; height: 28px; padding: 0 8px; border: 0; background: none; border-radius: 4px; color: var(--text); font-size: 13px; white-space: nowrap; }
.fb-btn:hover, .fb-icon:hover { background: var(--bg-hover); }
.fb-btn.chat { color: var(--accent); }
.fb-icon { width: 28px; height: 28px; border: 0; background: none; border-radius: 4px; color: var(--text); display: inline-flex; align-items: center; justify-content: center; }
.fb-icon.on { color: var(--accent); background: var(--accent-soft); }
.fb-a { font-weight: 600; text-decoration: underline; text-underline-offset: 3px; }
.fb-sep { width: 1px; height: 18px; background: var(--border); margin: 0 3px; }
.fb-link { display: flex; gap: 6px; background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 6px; z-index: 1300; }
.fb-link input { width: 260px; height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-s); padding: 0 8px; background: var(--bg); outline: none; }
.fb-link button { border: 0; background: var(--accent); color: #fff; border-radius: var(--radius-s); padding: 0 10px; }
.fb-colors { background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 8px; z-index: 1300; width: 236px; }
.fb-ctitle { font-size: 11.5px; color: var(--text-3); margin: 2px 2px 6px; }
.fb-cgrid { display: grid; grid-template-columns: repeat(5, 1fr); gap: 4px; margin-bottom: 8px; }
.fb-c { height: 32px; border: 1px solid var(--border); border-radius: 5px; background: var(--bg); font-weight: 600; }
.fb-c:hover { box-shadow: 0 0 0 1px var(--text-3); }
</style>
