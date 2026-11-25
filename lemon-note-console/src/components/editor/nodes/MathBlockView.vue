<script setup lang="ts">
import { computed, ref, nextTick } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import katex from 'katex'
import 'katex/dist/katex.min.css'
const props = defineProps(nodeViewProps)
const editing = ref(false)
const draft = ref('')
const ta = ref<HTMLTextAreaElement>()
const html = computed(() => {
  const s = props.node.attrs.latex || ''
  if (!s.trim()) return ''
  return katex.renderToString(s, { displayMode: true, throwOnError: false, strict: false, trust: false })
})
async function edit() {
  if (!props.editor.isEditable) return
  draft.value = props.node.attrs.latex || ''
  editing.value = true
  await nextTick()
  ta.value?.focus()
}
function done() {
  props.updateAttributes({ latex: draft.value })
  editing.value = false
}
</script>

<template>
  <NodeViewWrapper class="ln-math-block" :class="{ selected: props.selected }">
    <div v-if="html" class="render" @click="edit" v-html="html" />
    <div v-else class="placeholder" @click="edit">{{ $t('editor.math') }}</div>
    <div v-if="editing" class="editor" contenteditable="false">
      <textarea ref="ta" v-model="draft" spellcheck="false" rows="3" @keydown.enter.meta.prevent="done" @keydown.enter.ctrl.prevent="done" @keydown.esc="done" @blur="done" />
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-math-block { padding: 6px 0; border-radius: var(--radius-m); }
.ln-math-block.selected { background: var(--accent-soft); }
.render { cursor: pointer; overflow-x: auto; padding: 6px 4px; }
.render:hover { background: var(--bg-hover); border-radius: var(--radius-m); }
.placeholder { color: var(--text-3); background: var(--bg-soft); padding: 12px; border-radius: var(--radius-m); cursor: pointer; }
.editor textarea { width: 100%; font-family: var(--font-mono); font-size: 13px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 8px 10px; background: var(--bg); resize: vertical; outline: none; }
.editor textarea:focus { border-color: var(--accent); }
</style>
