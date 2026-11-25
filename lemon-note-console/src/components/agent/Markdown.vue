<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import { renderMarkdown } from '@/lib/markdown'
const props = defineProps<{ text: string }>()
const router = useRouter()
const html = computed(() => renderMarkdown(props.text))
function click(e: MouseEvent) {
  const a = (e.target as HTMLElement).closest('a[data-page]') as HTMLAnchorElement | null
  if (a && !e.metaKey && !e.ctrlKey) {
    e.preventDefault()
    router.push('/p/' + a.dataset.page)
  }
}
</script>

<template>
  <div class="md" @click="click" v-html="html" />
</template>

<style scoped>
.md { line-height: 1.65; word-wrap: break-word; }
.md :deep(p) { margin: 0 0 8px; }
.md :deep(p:last-child) { margin-bottom: 0; }
.md :deep(ul), .md :deep(ol) { padding-left: 1.4em; margin: 4px 0 8px; }
.md :deep(li) { margin: 2px 0; }
.md :deep(h1), .md :deep(h2), .md :deep(h3) { font-size: 1.05em; margin: 12px 0 6px; }
.md :deep(code) { font-family: var(--font-mono); font-size: .88em; background: var(--code-bg); color: var(--code-text); padding: .1em .3em; border-radius: 4px; }
.md :deep(pre) { background: var(--bg-sunken); border-radius: var(--radius-m); padding: 10px 12px; overflow-x: auto; font-size: 12.5px; }
.md :deep(pre code) { background: none; color: var(--text); padding: 0; }
.md :deep(blockquote) { margin: 6px 0; padding-left: 10px; border-left: 3px solid var(--border-strong); color: var(--text-2); }
.md :deep(table) { border-collapse: collapse; margin: 6px 0; font-size: 13px; }
.md :deep(td), .md :deep(th) { border: 1px solid var(--border-strong); padding: 4px 8px; }
.md :deep(a) { color: var(--accent); text-decoration: none; }
.md :deep(a:hover) { text-decoration: underline; }
.md :deep(a.ln-plink) { color: var(--text); font-weight: 500; text-decoration: underline; text-decoration-color: var(--border-strong); text-underline-offset: 3px; }
</style>
