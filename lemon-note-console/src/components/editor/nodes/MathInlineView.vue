<script setup lang="ts">
import { computed, ref, nextTick } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import katex from 'katex'
const props = defineProps(nodeViewProps)
const editing = ref(false)
const draft = ref('')
const input = ref<HTMLInputElement>()
const html = computed(() => katex.renderToString(props.node.attrs.latex || '?', { throwOnError: false, strict: false }))
async function edit() {
  if (!props.editor.isEditable) return
  draft.value = props.node.attrs.latex
  editing.value = true
  await nextTick()
  input.value?.focus()
}
function done() { props.updateAttributes({ latex: draft.value }); editing.value = false }
</script>

<template>
  <NodeViewWrapper as="span" class="ln-math-inline" :class="{ selected: props.selected }">
    <input v-if="editing" ref="input" v-model="draft" class="mi-input" @blur="done" @keydown.enter.prevent="done" @keydown.esc="done" />
    <span v-else @click="edit" v-html="html" />
  </NodeViewWrapper>
</template>

<style scoped>
.ln-math-inline { cursor: pointer; padding: 0 1px; border-radius: 3px; }
.ln-math-inline.selected, .ln-math-inline:hover { background: var(--bg-hover); }
.mi-input { font-family: var(--font-mono); font-size: 13px; border: 1px solid var(--accent); border-radius: 3px; padding: 0 4px; width: 14em; outline: none; background: var(--bg); }
</style>
