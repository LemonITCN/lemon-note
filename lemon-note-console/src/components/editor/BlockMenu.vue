<script setup lang="ts">
// 块菜单：拖拽把手点开，转换类型、颜色、复制、删除、添加到聊天。
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import type { Editor } from '@tiptap/core'
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuSub, DropdownMenuSubTrigger, DropdownMenuSubContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import { COLORS } from './extensions/marks'
import { newBlockId } from './extensions'
import { addToChat } from '@/lib/chatRefs'
import { useToast } from '@/stores/toast'

const props = defineProps<{ editor: Editor; pos: number | null; pageId: string; pageTitle: string }>()
const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const toast = useToast()
const node = computed(() => (props.pos === null ? null : props.editor.state.doc.nodeAt(props.pos)))

function turnInto(kind: string) {
  if (props.pos === null || !node.value) return
  const pos = props.pos
  const c = props.editor.chain().focus().setTextSelection(pos + 1)
  const m: Record<string, () => any> = {
    text: () => c.setParagraph(), h1: () => c.setHeading({ level: 1 }), h2: () => c.setHeading({ level: 2 }), h3: () => c.setHeading({ level: 3 }),
    bullet: () => c.toggleBulletList(), numbered: () => c.toggleOrderedList(), todo: () => c.toggleTaskList(), quote: () => c.toggleBlockquote(), code: () => c.toggleCodeBlock(),
  }
  m[kind]?.().run()
}

function color(c: string | null) {
  if (props.pos === null) return
  props.editor.chain().focus().setTextSelection(props.pos + 1).setBlockColor(c).run()
}

function duplicate() {
  if (props.pos === null || !node.value) return
  const json = node.value.toJSON()
  json.attrs = { ...(json.attrs || {}), id: newBlockId() }
  props.editor.chain().focus().insertContentAt(props.pos + node.value.nodeSize, json).run()
}

function remove() {
  if (props.pos === null || !node.value) return
  props.editor.chain().focus().deleteRange({ from: props.pos, to: props.pos + node.value.nodeSize }).run()
}

function copyLink() {
  const id = node.value?.attrs.id
  if (!id) return
  navigator.clipboard?.writeText(`${location.origin}/p/${props.pageId}#${id}`)
  toast.success(t('common.copied'))
}

function chat() {
  const n = node.value
  if (!n) return
  addToChat({ kind: 'selection', id: n.attrs.id, pageId: props.pageId, blockIds: [n.attrs.id], text: n.textContent.slice(0, 2000), title: props.pageTitle })
}

const turnItems = [['text', 'text'], ['h1', 'h1'], ['h2', 'h2'], ['h3', 'h3'], ['bullet', 'list-bullet'], ['numbered', 'list-number'], ['todo', 'list-check'], ['quote', 'quote'], ['code', 'code']]
</script>

<template>
  <DropdownMenuRoot v-model:open="open" :modal="false">
    <DropdownMenuTrigger as-child><slot /></DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent class="ln-menu" side="left" align="start" :side-offset="6" :collision-padding="8" style="min-width: 220px" @close-auto-focus.prevent>
        <DropdownMenuItem class="ln-menu-item" @select="chat"><Icon name="assistant" :size="16" /><span class="ln-menu-label">{{ t('editor.addToChat') }}</span></DropdownMenuItem>
        <DropdownMenuSeparator class="ln-menu-sep" />
        <DropdownMenuSub>
          <DropdownMenuSubTrigger class="ln-menu-item"><Icon name="refresh" :size="16" /><span class="ln-menu-label">{{ t('editor.turnInto') }}</span><Icon name="chevron-right" :size="14" /></DropdownMenuSubTrigger>
          <DropdownMenuPortal>
            <DropdownMenuSubContent class="ln-menu" :side-offset="4" style="min-width: 180px">
              <DropdownMenuItem v-for="[k, ic] in turnItems" :key="k" class="ln-menu-item" @select="turnInto(k)"><Icon :name="ic" :size="16" /><span class="ln-menu-label">{{ t('editor.' + k) }}</span></DropdownMenuItem>
            </DropdownMenuSubContent>
          </DropdownMenuPortal>
        </DropdownMenuSub>
        <DropdownMenuSub>
          <DropdownMenuSubTrigger class="ln-menu-item"><Icon name="palette" :size="16" /><span class="ln-menu-label">{{ t('editor.color') }}</span><Icon name="chevron-right" :size="14" /></DropdownMenuSubTrigger>
          <DropdownMenuPortal>
            <DropdownMenuSubContent class="ln-menu" :side-offset="4" style="min-width: 190px">
              <DropdownMenuItem class="ln-menu-item" @select="color(null)"><span class="swatch">A</span><span class="ln-menu-label">{{ t('colors.default') }}</span></DropdownMenuItem>
              <DropdownMenuItem v-for="c in COLORS" :key="c" class="ln-menu-item" @select="color(c)"><span class="swatch" :class="'c-' + c">A</span><span class="ln-menu-label">{{ t('colors.' + c) }}</span></DropdownMenuItem>
              <DropdownMenuSeparator class="ln-menu-sep" />
              <DropdownMenuItem v-for="c in COLORS" :key="'b' + c" class="ln-menu-item" @select="color(c + '_background')"><span class="swatch" :class="'cb-' + c">A</span><span class="ln-menu-label">{{ t('colors.' + c) }} · {{ t('editor.background') }}</span></DropdownMenuItem>
            </DropdownMenuSubContent>
          </DropdownMenuPortal>
        </DropdownMenuSub>
        <DropdownMenuItem class="ln-menu-item" @select="duplicate"><Icon name="duplicate" :size="16" /><span class="ln-menu-label">{{ t('editor.duplicateBlock') }}</span><span class="ln-menu-hint">⌘D</span></DropdownMenuItem>
        <DropdownMenuItem class="ln-menu-item" @select="copyLink"><Icon name="link" :size="16" /><span class="ln-menu-label">{{ t('editor.copyBlockLink') }}</span></DropdownMenuItem>
        <DropdownMenuSeparator class="ln-menu-sep" />
        <DropdownMenuItem class="ln-menu-item danger" @select="remove"><Icon name="trash" :size="16" /><span class="ln-menu-label">{{ t('editor.deleteBlock') }}</span><span class="ln-menu-hint">Del</span></DropdownMenuItem>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>

<style>
.swatch { width: 20px; height: 20px; border-radius: 4px; border: 1px solid var(--border); display: inline-flex; align-items: center; justify-content: center; font-size: 12px; font-weight: 600; }
</style>
