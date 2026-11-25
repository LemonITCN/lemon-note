<script setup lang="ts">
import { ref, watch, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Modal from '@/components/ui/Modal.vue'
import TextField from '@/components/ui/TextField.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import PageIcon from './PageIcon.vue'
import { api } from '@/api/client'
import type { PageMeta } from '@/api/types'
import { useWorkspace } from '@/stores/workspace'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatRelative } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'

const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const ws = useWorkspace()
const toast = useToast()
const router = useRouter()
const items = ref<PageMeta[]>([])
const q = ref('')
const shown = computed(() => items.value.filter((p) => !q.value || p.title.toLowerCase().includes(q.value.toLowerCase())))

async function load() {
  if (!ws.currentSpace) return
  try { items.value = (await api.get(`/api/spaces/${ws.currentSpace.space.id}/trash`)).pages.sort((a: PageMeta, b: PageMeta) => (b.trashed_at || 0) - (a.trashed_at || 0)) } catch (e) { toast.error(errorText(e)) }
}
watch(open, (v) => v && load())

async function restore(p: PageMeta) {
  try {
    await api.post(`/api/pages/${p.id}/restore`)
    await ws.loadTree(p.space_id, true)
    items.value = items.value.filter((x) => x.id !== p.id)
  } catch (e) { toast.error(errorText(e)) }
}

async function purge(p: PageMeta) {
  if (!(await confirmDialog({ title: t('page.deleteForever'), message: t('trash.confirmDelete'), danger: true, okText: t('page.deleteForever') }))) return
  try {
    await api.del(`/api/trash/${p.id}`)
    items.value = items.value.filter((x) => x.id !== p.id)
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <Modal v-model:open="open" :title="t('trash.title')" :width="520">
    <TextField v-model="q" :placeholder="t('trash.searchPlaceholder')" size="sm" />
    <div class="list">
      <p v-if="!shown.length" class="empty">{{ t('trash.empty') }}</p>
      <div v-for="p in shown" :key="p.id" class="row" @click="router.push(`/p/${p.id}`); open = false">
        <PageIcon :icon="p.icon" :kind="p.kind" :size="17" />
        <span class="title ln-ellipsis">{{ p.title || t('common.untitled') }}</span>
        <span class="time">{{ formatRelative(p.trashed_at || 0) }}</span>
        <IconBtn icon="undo" :label="t('page.restore')" @click.stop="restore(p)" />
        <IconBtn icon="trash" :label="t('page.deleteForever')" @click.stop="purge(p)" />
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.list { margin-top: 10px; min-height: 120px; }
.row { display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 6px; border-radius: 5px; cursor: pointer; }
.row:hover { background: var(--bg-hover); }
.title { flex: 1; }
.time { font-size: 12px; color: var(--text-3); }
.empty { color: var(--text-3); text-align: center; padding: 30px 0; }
</style>
