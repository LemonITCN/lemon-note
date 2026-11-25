<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import DocPreview from '@/components/editor/DocPreview.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatDate } from '@/lib/time'
import { useWorkspace } from '@/stores/workspace'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ pageId: string; editable: boolean }>()
const emit = defineEmits<{ restored: [] }>()
const { t } = useI18n()
const toast = useToast()
const ws = useWorkspace()
const versions = ref<any[]>([])
const sel = ref('')
const preview = ref<any>(null)
const busy = ref(false)

watch(open, async (v) => {
  if (!v) return
  preview.value = null
  try {
    versions.value = (await api.get(`/api/pages/${props.pageId}/versions`)).versions
    if (versions.value[0]) pick(versions.value[0].id)
  } catch (e) { toast.error(errorText(e)) }
})

async function pick(id: string) {
  sel.value = id
  try { preview.value = await api.get(`/api/pages/${props.pageId}/versions/${id}`) } catch (e) { toast.error(errorText(e)) }
}

async function restore() {
  busy.value = true
  try {
    await api.post(`/api/pages/${props.pageId}/versions/${sel.value}/restore`)
    toast.success(t('history.restored'))
    open.value = false
    emit('restored')
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

function who(id: string) { return id === 'system' ? 'system' : '' }
void ws
</script>

<template>
  <Modal v-model:open="open" :title="t('history.title')" width="min(1100px, 96vw)" :padded="false">
    <div class="hp">
      <div class="preview">
        <div v-if="preview" class="doc">
          <h1 class="ptitle">{{ preview.page.title || t('common.untitled') }}</h1>
          <DocPreview :blocks="preview.blocks.map((b: any) => b.data)" :page-id="pageId" />
        </div>
        <p v-else-if="!versions.length" class="empty">{{ t('history.empty') }}</p>
      </div>
      <div class="list">
        <div v-for="v in versions" :key="v.id" class="ver" :class="{ on: v.id === sel }" @click="pick(v.id)">
          <div class="vt">{{ formatDate(v.created_at, true) }}</div>
          <div class="vr">{{ t('history.reasons.' + v.reason) || v.reason }}{{ who(v.created_by) }}</div>
        </div>
        <div class="foot"><Btn variant="primary" block :disabled="!sel || !editable" :loading="busy" @click="restore">{{ t('history.restore') }}</Btn></div>
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.hp { display: flex; height: 70vh; border-top: 1px solid var(--border); }
.preview { flex: 1; overflow: auto; padding: 28px 48px; min-width: 0; }
.ptitle { font-size: 32px; margin: 0 0 16px; }
.list { width: 260px; border-left: 1px solid var(--border); display: flex; flex-direction: column; background: var(--bg-soft); }
.ver { padding: 10px 14px; cursor: pointer; border-bottom: 1px solid var(--border); }
.ver:hover { background: var(--bg-hover); }
.ver.on { background: var(--accent-soft); }
.vt { font-weight: 500; font-size: 13.5px; }
.vr { font-size: 12px; color: var(--text-3); margin-top: 2px; }
.foot { margin-top: auto; padding: 12px; border-top: 1px solid var(--border); }
.empty { color: var(--text-3); text-align: center; margin-top: 60px; }
@media (max-width: 760px) { .hp { flex-direction: column-reverse; height: 80vh; } .list { width: 100%; height: 40%; border-left: 0; border-top: 1px solid var(--border); overflow: auto; } .preview { padding: 16px; } }
</style>
