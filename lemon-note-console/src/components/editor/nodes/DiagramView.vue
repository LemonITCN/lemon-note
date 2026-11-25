<script setup lang="ts">
// Mermaid 与 Graphviz 共用：源码 + 预览，重型库按需加载。
import { ref, watch, onMounted, computed, nextTick } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import { sanitizeSVG } from '@/lib/sanitize'
import { useUI } from '@/stores/ui'
import { renderMermaid, renderGraphviz } from '@/lib/diagrams'

const props = defineProps(nodeViewProps)
const ui = useUI()
const svg = ref('')
const error = ref('')
const editing = ref(false)
const draft = ref(props.node.attrs.code || '')
const ta = ref<HTMLTextAreaElement>()
const isMermaid = computed(() => props.node.type.name === 'mermaid')
let seq = 0

async function render(code: string) {
  const my = ++seq
  if (!code.trim()) { svg.value = ''; error.value = ''; return }
  try {
    const out = isMermaid.value ? await renderMermaid(code, ui.effectiveTheme) : await renderGraphviz(code, props.node.attrs.engine || 'dot')
    if (my !== seq) return
    svg.value = sanitizeSVG(out)
    error.value = ''
  } catch (e: any) {
    if (my !== seq) return
    error.value = String(e?.message || e).slice(0, 500)
  }
}

let timer: number | undefined
watch(draft, (v) => {
  clearTimeout(timer)
  timer = window.setTimeout(() => { render(v); props.updateAttributes({ code: v }) }, 350)
})
watch(() => props.node.attrs.code, (v) => { if (!editing.value) { draft.value = v; render(v) } })
watch(() => ui.effectiveTheme, () => render(props.node.attrs.code || ''))
onMounted(() => {
  render(props.node.attrs.code || '')
  if (!props.node.attrs.code && props.editor.isEditable) startEdit()
})

async function startEdit() {
  if (!props.editor.isEditable) return
  editing.value = true
  await nextTick()
  ta.value?.focus()
}

function onTab(e: KeyboardEvent) {
  const el = e.target as HTMLTextAreaElement
  const s = el.selectionStart
  draft.value = draft.value.slice(0, s) + '  ' + draft.value.slice(el.selectionEnd)
  nextTick(() => el.setSelectionRange(s + 2, s + 2))
}
</script>

<template>
  <NodeViewWrapper class="ln-diagram" :class="{ selected: props.selected, editing }">
    <div class="bar" contenteditable="false">
      <span class="kind">{{ isMermaid ? 'Mermaid' : 'Graphviz' }}</span>
      <span class="ln-spacer" />
      <button v-if="props.editor.isEditable" class="tb" @click="editing ? (editing = false) : startEdit()">
        <Icon :name="editing ? 'eye' : 'code'" :size="14" />{{ editing ? $t('editor.preview') : $t('editor.editSource') }}
      </button>
    </div>
    <div v-if="editing" class="src" contenteditable="false">
      <textarea ref="ta" v-model="draft" spellcheck="false" @keydown.tab.prevent="onTab" @keydown.esc="editing = false" />
    </div>
    <div class="preview" contenteditable="false" @dblclick="startEdit">
      <div v-if="svg" class="svg" v-html="svg" />
      <div v-else-if="!error" class="empty">{{ isMermaid ? $t('editor.mermaid') : $t('editor.graphviz') }}</div>
      <pre v-if="error" class="err">{{ $t('editor.renderError') }}：{{ error }}</pre>
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-diagram { border: 1px solid var(--border); border-radius: var(--radius-l); margin: 6px 0; overflow: hidden; background: var(--bg); }
.ln-diagram.selected { box-shadow: 0 0 0 2px var(--accent-soft); border-color: var(--accent); }
.bar { display: flex; align-items: center; height: 30px; padding: 0 6px 0 12px; font-size: 12px; color: var(--text-3); border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.tb { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12px; padding: 3px 6px; border-radius: 4px; }
.tb:hover { background: var(--bg-hover); color: var(--text); }
.src textarea { display: block; width: 100%; min-height: 140px; border: 0; border-bottom: 1px solid var(--border); padding: 10px 12px; font-family: var(--font-mono); font-size: 13px; line-height: 1.55; resize: vertical; outline: none; background: var(--bg-sunken); color: var(--text); tab-size: 2; }
.preview { padding: 14px; overflow-x: auto; min-height: 60px; }
.svg { display: flex; justify-content: center; }
.svg :deep(svg) { max-width: 100%; height: auto; }
.empty { color: var(--text-3); text-align: center; padding: 18px 0; }
.err { color: var(--danger); font-size: 12px; white-space: pre-wrap; margin: 0; font-family: var(--font-mono); }
</style>
