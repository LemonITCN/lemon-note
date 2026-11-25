<script setup lang="ts">
// 备份设置（管理员）：备份目标、最近运行。
import { ref, onMounted, onBeforeUnmount, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Btn from '@/components/ui/Btn.vue'
import Menu from '@/components/ui/Menu.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import TargetEditor from './backup/TargetEditor.vue'
import BackupList from './backup/BackupList.vue'
import RunDialog from './backup/RunDialog.vue'
import BaiduAuth from './backup/BaiduAuth.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatDate, formatRelative } from '@/lib/time'
import { formatSize } from '@/lib/files'
import { confirmDialog } from '@/lib/confirm'

const { t } = useI18n()
const toast = useToast()
const targets = ref<any[]>([])
const runs = ref<any[]>([])
const loaded = ref(false)
const editOpen = ref(false)
const editing = ref<any>(null)
const listOpen = ref(false)
const listTarget = ref<any>(null)
const runOpen = ref(false)
const runId = ref('')
const baiduOpen = ref(false)
const baiduId = ref('')
let timer: number | undefined

async function load() {
  try {
    const [a, b] = await Promise.all([api.get('/api/admin/backup/targets'), api.get('/api/admin/backup/runs?limit=20')])
    targets.value = a.targets || []
    runs.value = b.runs || []
  } catch (e) { toast.error(errorText(e)) }
  loaded.value = true
  clearTimeout(timer)
  const busy = targets.value.some((x) => x.running) || runs.value.some((x) => x.running)
  timer = window.setTimeout(load, busy ? 2000 : 15000)
}
onMounted(load)
onBeforeUnmount(() => clearTimeout(timer))

function schedText(s: any) {
  switch (s?.kind) {
    case 'hourly': return t('backup.schHourly', { n: s.every_hours })
    case 'daily': return t('backup.schDaily', { at: s.at })
    case 'weekly': return t('backup.schWeekly', { days: (s.weekdays || []).map((d: number) => t('backup.wd.' + d)).join(' '), at: s.at })
    default: return t('backup.sch.manual')
  }
}
function pct(p: any) {
  if (!p) return 0
  if (p.bytes_total) return Math.min(100, Math.round((p.bytes_done / p.bytes_total) * 100))
  if (p.files_total) return Math.min(100, Math.round((p.files_done / p.files_total) * 100))
  return 0
}
function showRun(id: string) { runId.value = id; runOpen.value = true; load() }
async function runNow(x: any) {
  try { const r = await api.post(`/api/admin/backup/targets/${x.id}/run`); showRun(r.run_id) } catch (e) { toast.error(errorText(e)) }
}
async function test(x: any) {
  try {
    const r = await api.post(`/api/admin/backup/targets/${x.id}/test`)
    if (r.ok) toast.success(t('backup.testOk') + (r.warning ? '：' + r.warning : ''))
    else toast.error(r.host_key ? t('backup.hostKey') + ' ' + r.host_key : r.error)
  } catch (e) { toast.error(errorText(e)) }
}
async function remove(x: any) {
  if (!(await confirmDialog({ title: t('backup.deleteTarget'), message: t('backup.deleteTargetMsg', { name: x.name }), danger: true }))) return
  try { await api.del(`/api/admin/backup/targets/${x.id}`); load() } catch (e) { toast.error(errorText(e)) }
}
async function toggle(x: any) {
  try { await api.put(`/api/admin/backup/targets/${x.id}`, { enabled: !x.enabled }); load() } catch (e) { toast.error(errorText(e)) }
}
function menu(x: any) {
  return [
    { label: t('backup.test'), icon: 'refresh', run: () => test(x) },
    ...(x.kind === 'baidu' ? [{ label: t('backup.baiduAuth'), icon: 'key', run: () => { baiduId.value = x.id; baiduOpen.value = true } }] : []),
    { label: x.enabled ? t('backup.disable') : t('backup.enable'), icon: x.enabled ? 'eye-off' : 'eye', run: () => toggle(x) },
    { label: t('common.delete'), icon: 'trash', danger: true, run: () => remove(x) },
  ]
}
function saved(v: any) {
  load()
  if (v?.kind === 'baidu' && !editing.value) { baiduId.value = v.id; baiduOpen.value = true }
}
const empty = computed(() => loaded.value && !targets.value.length)
</script>

<template>
  <Section :title="t('backup.targets')" :tip="t('backup.targetsTip')">
    <div v-if="empty" class="empty">
      <Icon name="cloud-up" :size="28" />
      <span>{{ t('backup.noTargets') }}</span>
    </div>
    <div v-for="x in targets" :key="x.id" class="tg" :class="{ off: !x.enabled }">
      <div class="th">
        <div class="ti">
          <div class="tn">{{ x.name }}<span class="tag">{{ t('backup.kinds.' + x.kind) }}</span><span class="tag">{{ x.mode === 'mirror' ? t('backup.modeMirror') : t('backup.modeFull') }}</span>
            <span v-if="x.encryption.enabled" class="tag"><Icon name="lock" :size="11" />{{ t('backup.encrypted') }}</span>
            <span v-if="!x.enabled" class="tag">{{ t('backup.disabled') }}</span>
          </div>
          <div class="tm">
            {{ schedText(x.schedule) }}
            <template v-if="x.last_run_at"> · <span :class="'s-' + x.last_status">{{ t('backup.status.' + (x.last_status || 'success')) }}</span> {{ formatRelative(x.last_run_at) }}</template>
            <template v-if="x.next_run_at && x.enabled"> · {{ t('backup.next', { at: formatDate(x.next_run_at, true) }) }}</template>
          </div>
        </div>
        <Btn size="sm" :disabled="!!x.running" @click="runNow(x)">{{ t('backup.runNow') }}</Btn>
        <Btn size="sm" variant="ghost" @click="listTarget = x; listOpen = true">{{ t('backup.backups') }}</Btn>
        <IconBtn icon="edit" :size="15" :label="t('common.edit')" @click="editing = x; editOpen = true" />
        <Menu :items="menu(x)" align="end"><IconBtn icon="more" :size="15" :label="t('common.more')" /></Menu>
      </div>
      <button v-if="x.running" class="prog" @click="showRun(x.running.id)">
        <span class="ph">{{ x.running.progress.phase }}</span>
        <span class="bar"><span class="fill" :style="{ width: pct(x.running.progress) + '%' }" /></span>
        <span class="pc">{{ pct(x.running.progress) }}%</span>
      </button>
    </div>
    <div><Btn size="sm" variant="primary" icon="plus" @click="editing = null; editOpen = true">{{ t('backup.newTarget') }}</Btn></div>
  </Section>
  <Section v-if="runs.length" :title="t('backup.recentRuns')">
    <table class="runs">
      <tr v-for="r in runs" :key="r.id" @click="showRun(r.id)">
        <td class="tm">{{ formatDate(r.started_at, true) }}</td>
        <td>{{ r.target_name }}</td>
        <td>{{ t('backup.runKinds.' + r.kind) }}</td>
        <td><span :class="'s-' + r.status">{{ t('backup.status.' + r.status) }}</span></td>
        <td class="tm">{{ r.bytes ? formatSize(r.bytes) : '' }}</td>
      </tr>
    </table>
  </Section>
  <TargetEditor v-model:open="editOpen" :target="editing" @saved="saved" />
  <BackupList v-if="listTarget" v-model:open="listOpen" :target="listTarget" @run="(id: string) => { listOpen = false; showRun(id) }" />
  <RunDialog v-if="runId" v-model:open="runOpen" :run-id="runId" @finished="load" />
  <BaiduAuth v-if="baiduId" v-model:open="baiduOpen" :target-id="baiduId" @authorized="load" />
</template>

<style scoped>
.empty { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 24px 0; color: var(--text-3); font-size: 13px; }
.tg { border: 1px solid var(--border); border-radius: var(--radius-m); padding: 10px 12px; display: flex; flex-direction: column; gap: 8px; }
.tg.off { opacity: .65; }
.th { display: flex; align-items: center; gap: 6px; }
.ti { flex: 1; min-width: 0; }
.tn { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 13.5px; flex-wrap: wrap; }
.tag { font-size: 11px; font-weight: 400; background: var(--bg-hover); color: var(--text-2); border-radius: 4px; padding: 0 5px; display: inline-flex; align-items: center; gap: 3px; }
.tm { font-size: 12px; color: var(--text-3); }
.s-success { color: var(--success); }
.s-failed { color: var(--danger); }
.s-warning { color: var(--warning); }
.s-running { color: var(--accent); }
.s-canceled { color: var(--text-3); }
.prog { display: flex; align-items: center; gap: 10px; border: 0; background: var(--bg-soft); border-radius: var(--radius-m); padding: 6px 10px; text-align: left; color: var(--text-2); font-size: 12px; }
.ph { flex: 0 1 45%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.bar { flex: 1; height: 5px; border-radius: 3px; background: var(--bg-hover); overflow: hidden; }
.fill { display: block; height: 100%; background: var(--accent); transition: width .3s; }
.pc { width: 36px; text-align: right; }
.runs { width: 100%; border-collapse: collapse; font-size: 12.5px; }
.runs tr { cursor: pointer; }
.runs tr:hover td { background: var(--bg-hover); }
.runs td { padding: 6px; border-bottom: 1px solid var(--border); }
@media (max-width: 760px) { .th { flex-wrap: wrap; } .ti { flex-basis: 100%; } }
</style>
