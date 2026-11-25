<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'

const props = defineProps(nodeViewProps)
const items = ref<{ level: number; text: string; pos: number }[]>([])

function collect() {
  const out: { level: number; text: string; pos: number }[] = []
  props.editor.state.doc.descendants((n, pos) => {
    if (n.type.name === 'heading') out.push({ level: n.attrs.level, text: n.textContent, pos })
    if (n.type.name === 'toggle' && n.attrs.level) out.push({ level: n.attrs.level, text: n.firstChild?.textContent || '', pos })
  })
  items.value = out
}

function go(pos: number) {
  const dom = props.editor.view.nodeDOM(pos) as HTMLElement | null
  dom?.scrollIntoView({ behavior: 'smooth', block: 'start' })
}

onMounted(() => { collect(); props.editor.on('update', collect) })
onBeforeUnmount(() => props.editor.off('update', collect))
</script>

<template>
  <NodeViewWrapper class="ln-toc" :class="{ selected: props.selected }" contenteditable="false">
    <div v-for="(it, i) in items" :key="i" class="it" :style="{ paddingLeft: (it.level - 1) * 18 + 'px' }" @click="go(it.pos)">{{ it.text || $t('common.untitled') }}</div>
    <div v-if="!items.length" class="empty">{{ $t('editor.toc') }}</div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-toc { margin: 4px 0; padding: 2px 0; border-radius: var(--radius-m); }
.ln-toc.selected { background: var(--accent-soft); }
.it { color: var(--text-2); cursor: pointer; padding: 3px 6px; border-radius: 4px; text-decoration: underline; text-decoration-color: var(--border-strong); text-underline-offset: 3px; }
.it:hover { background: var(--bg-hover); color: var(--text); }
.empty { color: var(--text-3); padding: 4px 6px; }
</style>
