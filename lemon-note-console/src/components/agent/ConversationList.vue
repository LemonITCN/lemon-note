<script setup lang="ts">
import { onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import IconBtn from '@/components/ui/IconBtn.vue'
import { useAgent } from '@/stores/agent'
import { formatRelative } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'

const emit = defineEmits<{ pick: [id: string] }>()
const { t } = useI18n()
const agent = useAgent()
onMounted(() => agent.loadList())

async function rename(id: string, old: string) {
  const v = window.prompt(t('common.rename'), old)
  if (v && v.trim()) await agent.rename(id, v.trim())
}
async function remove(id: string, title: string) {
  if (await confirmDialog({ title: t('common.delete'), message: title, danger: true, okText: t('common.delete') })) await agent.remove(id)
}
</script>

<template>
  <div class="cl">
    <div v-for="c in agent.conversations" :key="c.id" class="row" :class="{ on: c.id === agent.currentId }" @click="emit('pick', c.id)">
      <span v-if="c.running" class="live" />
      <span class="tt ln-ellipsis">{{ c.title || t('agent.newChat') }}</span>
      <span class="tm">{{ formatRelative(c.updated_at) }}</span>
      <span class="acts" @click.stop>
        <IconBtn icon="text" :size="14" :label="t('common.rename')" @click="rename(c.id, c.title)" />
        <IconBtn icon="trash" :size="14" :label="t('common.delete')" @click="remove(c.id, c.title)" />
      </span>
    </div>
    <p v-if="!agent.conversations.length" class="empty">{{ t('common.none') }}</p>
  </div>
</template>

<style scoped>
.cl { flex: 1; overflow-y: auto; padding: 6px; }
.row { display: flex; align-items: center; gap: 6px; height: 34px; padding: 0 8px; border-radius: 6px; cursor: pointer; }
.row:hover { background: var(--bg-hover); }
.row.on { background: var(--bg-active); }
.tt { flex: 1; font-size: 13.5px; }
.tm { font-size: 11.5px; color: var(--text-3); }
.acts { display: none; }
.row:hover .acts { display: flex; }
.row:hover .tm { display: none; }
.acts :deep(.icon-btn) { width: 22px; height: 22px; }
.live { width: 7px; height: 7px; border-radius: 50%; background: var(--accent); flex: none; animation: pulse 1.2s infinite; }
@keyframes pulse { 50% { opacity: .3; } }
.empty { color: var(--text-3); text-align: center; margin-top: 40px; }
</style>
