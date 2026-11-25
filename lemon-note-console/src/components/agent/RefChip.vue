<script setup lang="ts">
import { computed } from 'vue'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import type { ChatRef } from '@/lib/chatRefs'

const props = defineProps<{ r: ChatRef; removable?: boolean }>()
const emit = defineEmits<{ remove: [] }>()
const router = useRouter()
const label = computed(() => {
  if (props.r.kind === 'selection') return (props.r.text || '').replace(/\s+/g, ' ').slice(0, 24) + ((props.r.text || '').length > 24 ? '…' : '')
  return props.r.title || '无标题'
})
const tag = computed(() => ({ page: '页面', database: '数据库', row: '行', selection: '段落', file: '文件', view: '视图' } as Record<string, string>)[props.r.kind] || props.r.kind)
function go() {
  const id = props.r.kind === 'selection' ? props.r.pageId : props.r.id
  if (id) router.push('/p/' + id)
}
</script>

<template>
  <span class="chip" :title="r.kind === 'selection' ? r.text : r.title" @click="go">
    <span class="tag">{{ tag }}</span>
    <PageIcon v-if="r.kind === 'page' || r.kind === 'database' || r.kind === 'row'" :icon="r.icon" :kind="r.kind === 'database' ? 'database' : 'page'" :size="13" />
    <span class="lbl">{{ label }}</span>
    <button v-if="removable" class="x" @click.stop="emit('remove')"><Icon name="close" :size="11" /></button>
  </span>
</template>

<style scoped>
.chip { display: inline-flex; align-items: center; gap: 4px; max-width: 220px; height: 22px; padding: 0 6px 0 3px; border-radius: 5px; background: var(--bg-elev); border: 1px solid var(--border-strong); font-size: 12px; cursor: pointer; }
.tag { font-size: 10.5px; padding: 0 4px; border-radius: 3px; background: var(--accent); color: #fff; line-height: 16px; flex: none; }
.lbl { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--accent); }
.x { border: 0; background: none; color: var(--text-3); padding: 0; display: flex; border-radius: 3px; }
.x:hover { color: var(--text); }
</style>
