<script setup lang="ts">
// 一次运行（备份 / 校验 / 恢复）的进度、日志与结果。运行中每 1.5 秒刷新。
import { ref, watch, onBeforeUnmount, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import { api } from '@/api/client'
import { formatDate } from '@/lib/time'
import { formatSize } from '@/lib/files'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ runId: string }>()
const emit = defineEmits<{ finished: [] }>()
const { t } = useI18n()
const toast = useToast()
const run = ref<any>(null)
let timer: number | undefined

async function load() {
  try {
    const was = run.value?.running
    run.value = await api.get(`/api/admin/backup/runs/${props.runId}`)
    if (was && !run.value.running) emit('finished')
  } catch (e) { toast.error(errorText(e)) }
  clearTimeout(timer)
  if (open.value && run.value?.running) timer = window.setTimeout(load, 1500)
}
watch(open, (v) => { if (v && props.runId) { run.value = null; load() } else clearTimeout(timer) }, { immediate: true })
onBeforeUnmount(() => clearTimeout(timer))

const pct = computed(() => {
  const p = run.value?.progress
  if (!p) return 0
  if (p.bytes_total) return Math.min(100, Math.round((p.bytes_done / p.bytes_total) * 100))
  if (p.files_total) return Math.min(100, Math.round((p.files_done / p.files_total) * 100))
  return 0
})
async function cancel() { try { await api.post(`/api/admin/backup/runs/${props.runId}/cancel`); load() } catch (e) { toast.error(errorText(e)) } }
</script>

<template>
  <Modal v-model:open="open" :title="run ? t('backup.runKinds.' + run.kind) + ' · ' + run.target_name : ''" :width="640">
    <div v-if="run" class="rd">
      <div class="meta">
        <span class="st" :class="run.status">{{ t('backup.status.' + run.status) }}</span>
        <span>{{ formatDate(run.started_at, true) }}</span>
        <span v-if="run.files">{{ run.files }} {{ t('backup.files') }} · {{ formatSize(run.bytes) }}</span>
      </div>
      <div v-if="run.running && run.progress" class="prog">
        <div class="ph">{{ run.progress.phase }}<span class="ln-spacer" />{{ pct }}%</div>
        <div class="bar"><div class="fill" :style="{ width: pct + '%' }" /></div>
      </div>
      <div v-if="run.error" class="err">{{ run.error }}</div>
      <template v-if="run.result">
        <div v-if="run.kind === 'restore' && run.result.hint" class="hint">{{ run.result.hint }}<div v-if="run.result.dir" class="dir"><code>{{ run.result.dir }}</code></div></div>
        <div v-if="run.kind === 'verify'" class="hint" :class="{ bad: !run.result.ok }">{{ run.result.ok ? t('backup.verifyOk', { n: run.result.files }) : t('backup.verifyBad') }}</div>
        <ul v-if="run.result.problems?.length" class="probs"><li v-for="p in run.result.problems" :key="p">{{ p }}</li></ul>
        <ul v-if="run.result.warnings?.length" class="probs"><li v-for="p in run.result.warnings" :key="p">{{ p }}</li></ul>
      </template>
      <pre v-if="run.log" class="log">{{ run.log }}</pre>
    </div>
    <template #footer>
      <Btn v-if="run?.running" variant="danger" @click="cancel">{{ t('backup.cancelRun') }}</Btn>
      <Btn @click="open = false">{{ t('common.close') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.rd { display: flex; flex-direction: column; gap: 12px; }
.meta { display: flex; gap: 12px; align-items: center; font-size: 13px; color: var(--text-2); flex-wrap: wrap; }
.st { font-size: 12px; padding: 1px 8px; border-radius: 10px; background: var(--bg-hover); }
.st.success { color: var(--success); background: rgba(46, 160, 67, .12); }
.st.failed { color: var(--danger); background: rgba(220, 60, 60, .12); }
.st.warning { color: var(--warning); background: rgba(194, 124, 14, .12); }
.st.running { color: var(--accent); background: var(--accent-soft); }
.prog .ph { display: flex; font-size: 12.5px; color: var(--text-2); margin-bottom: 6px; }
.bar { height: 6px; background: var(--bg-hover); border-radius: 3px; overflow: hidden; }
.fill { height: 100%; background: var(--accent); transition: width .3s; }
.err { color: var(--danger); font-size: 13px; }
.hint { font-size: 13px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 10px 12px; line-height: 1.6; }
.hint.bad { color: var(--danger); }
.dir code { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
.probs { margin: 0; padding-left: 18px; font-size: 12.5px; color: var(--warning); max-height: 140px; overflow: auto; }
.log { margin: 0; max-height: 280px; overflow: auto; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 10px; font-family: var(--font-mono); font-size: 11.5px; line-height: 1.55; white-space: pre-wrap; word-break: break-all; }
</style>
