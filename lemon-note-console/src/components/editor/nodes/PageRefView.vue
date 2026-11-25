<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import { useRouter } from 'vue-router'
import PageIcon from '@/components/page/PageIcon.vue'
import Icon from '@/components/icons/Icon.vue'
import { useWorkspace } from '@/stores/workspace'
import { api } from '@/api/client'
import DatabaseEmbed from '@/components/database/DatabaseEmbed.vue'

const props = defineProps(nodeViewProps)
const ws = useWorkspace()
const router = useRouter()
const fetched = ref<{ title: string; icon: string; kind: string } | null>(null)
const missing = ref(false)
const kind = computed(() => props.node.type.name)
const meta = computed(() => ws.pages.get(props.node.attrs.pageId) || fetched.value)

onMounted(async () => {
  const id = props.node.attrs.pageId
  if (!id || ws.pages.has(id) || kind.value === 'childDatabase') return
  try {
    const r = await api.get(`/api/pages/${id}`)
    fetched.value = { title: r.page.title, icon: r.page.icon, kind: r.page.kind }
  } catch { missing.value = true }
})

function go() { if (props.node.attrs.pageId) router.push(`/p/${props.node.attrs.pageId}`) }
</script>

<template>
  <NodeViewWrapper class="ln-pageref" :class="[kind, { selected: props.selected }]">
    <DatabaseEmbed v-if="kind === 'childDatabase'" :database-id="props.node.attrs.pageId" :view-id="props.node.attrs.viewId" :editable="props.editor.isEditable" />
    <div v-else class="row" contenteditable="false" @click="go">
      <Icon v-if="kind === 'linkToPage'" name="arrow-up-right" :size="14" class="arrow" />
      <PageIcon :icon="meta?.icon" :kind="meta?.kind" :size="19" />
      <span class="title" :class="{ missing }">{{ missing ? '无权访问或已删除' : (meta?.title || $t('common.untitled')) }}</span>
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-pageref { margin: 1px 0; }
.row { display: inline-flex; align-items: center; gap: 6px; padding: 3px 6px; margin-left: -6px; border-radius: 5px; cursor: pointer; max-width: 100%; }
.row:hover { background: var(--bg-hover); }
.ln-pageref.selected .row { background: var(--accent-soft); }
.title { font-weight: 500; border-bottom: 1px solid var(--border-strong); line-height: 1.3; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.title.missing { color: var(--text-3); border-bottom: 0; font-weight: 400; }
.arrow { color: var(--text-3); margin-right: -2px; }
</style>
