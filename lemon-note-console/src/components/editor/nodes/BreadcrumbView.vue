<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { useWorkspace } from '@/stores/workspace'
import { pageContext } from '../context'

const props = defineProps(nodeViewProps)
const ws = useWorkspace()
const chain = computed(() => {
  const out: { id: string; title: string }[] = []
  let p = ws.pages.get(pageContext(props.editor)?.pageId || '')
  let guard = 0
  while (p && guard++ < 30) { out.unshift({ id: p.id, title: p.title }); p = p.parent_id ? ws.pages.get(p.parent_id) : undefined }
  return out
})
</script>

<template>
  <NodeViewWrapper class="ln-bc" :class="{ selected: props.selected }" contenteditable="false">
    <template v-for="(c, i) in chain" :key="c.id">
      <span v-if="i" class="sep">/</span>
      <RouterLink :to="`/p/${c.id}`" class="c">{{ c.title || $t('common.untitled') }}</RouterLink>
    </template>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-bc { display: flex; flex-wrap: wrap; gap: 4px; color: var(--text-2); font-size: 14px; padding: 4px 0; }
.ln-bc.selected { background: var(--accent-soft); border-radius: 4px; }
.c { color: inherit; text-decoration: none; padding: 1px 4px; border-radius: 4px; }
.c:hover { background: var(--bg-hover); }
.sep { color: var(--text-3); }
</style>
