<script setup lang="ts">
import { computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { useRouter } from 'vue-router'
import PageIcon from '@/components/page/PageIcon.vue'
import { useWorkspace } from '@/stores/workspace'

const props = defineProps(nodeViewProps)
const ws = useWorkspace()
const router = useRouter()
const a = computed(() => props.node.attrs)
const page = computed(() => (a.value.kind === 'page' ? ws.pages.get(a.value.id) : undefined))
const text = computed(() => {
  if (a.value.kind === 'date') return a.value.endDate ? `${a.value.date} → ${a.value.endDate}` : a.value.date
  if (a.value.kind === 'page') return page.value?.title || a.value.label || ''
  return a.value.label || ''
})
function click() { if (a.value.kind === 'page' && a.value.id) router.push(`/p/${a.value.id}`) }
</script>

<template>
  <NodeViewWrapper as="span" class="ln-mention" :class="['k-' + a.kind, { selected: props.selected }]" @click="click">
    <PageIcon v-if="a.kind === 'page'" :icon="page?.icon" :size="15" class="mi" />
    <span v-if="a.kind === 'user'" class="at">@</span>{{ text }}
  </NodeViewWrapper>
</template>

<style scoped>
.ln-mention { display: inline-flex; align-items: baseline; gap: 2px; border-radius: 3px; padding: 0 2px; cursor: pointer; }
.ln-mention:hover, .ln-mention.selected { background: var(--bg-hover); }
.k-page { font-weight: 500; text-decoration: underline; text-decoration-color: var(--border-strong); text-underline-offset: 3px; }
.mi { transform: translateY(2px); }
.k-user, .k-date { color: var(--text-2); }
.at { color: var(--text-3); }
</style>
