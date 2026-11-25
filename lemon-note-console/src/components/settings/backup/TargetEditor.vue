<script setup lang="ts">
// 新建 / 编辑备份目标。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { KINDS, KIND_ORDER, MASK, defaults } from './kinds'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: any | null }>()
const emit = defineEmits<{ saved: [t: any] }>()
const { t, te } = useI18n()
const toast = useToast()

const name = ref('')
const kind = ref('local')
const config = ref<Record<string, any>>({})
const mode = ref('full')
const schedule = ref<any>({ kind: 'daily', at: '03:00', every_hours: 6, weekdays: [0] })
const retention = ref<any>({ kind: 'last', keep_last: 7, daily: 7, weekly: 4, monthly: 6, volume_mb: 1024 })
const encEnabled = ref(false)
const encPassword = ref('')
const hasPassword = ref(false)
const enabled = ref(true)
const busy = ref(false)
const testing = ref(false)
const test = ref<any>(null)

watch(open, (v) => {
  if (!v) return
  test.value = null
  encPassword.value = ''
  const x = props.target
  if (x) {
    name.value = x.name; kind.value = x.kind; config.value = { ...defaults(x.kind), ...x.config }; mode.value = x.mode
    schedule.value = { every_hours: 6, at: '03:00', weekdays: [0], ...x.schedule }
    retention.value = { keep_last: 7, daily: 7, weekly: 4, monthly: 6, volume_mb: 1024, ...x.retention }
    encEnabled.value = x.encryption.enabled; hasPassword.value = x.encryption.has_password; enabled.value = x.enabled
  } else {
    name.value = ''; kind.value = 'local'; config.value = defaults('local'); mode.value = 'full'
    schedule.value = { kind: 'daily', at: '03:00', every_hours: 6, weekdays: [0] }
    retention.value = { kind: 'last', keep_last: 7, daily: 7, weekly: 4, monthly: 6, volume_mb: 1024 }
    encEnabled.value = false; hasPassword.value = false; enabled.value = true
  }
}, { immediate: true })

function setKind(k: string) {
  if (k === kind.value) return
  kind.value = k
  config.value = defaults(k)
  test.value = null
}

const fields = computed(() => KINDS[kind.value] || [])
const modes = computed(() => [{ value: 'full', label: t('backup.modeFull') }, { value: 'mirror', label: t('backup.modeMirror') }])
const retKinds = computed(() => [{ value: 'last', label: t('backup.keepLast') }, { value: 'tiered', label: t('backup.tiered') }])
const weekdays = computed(() => [1, 2, 3, 4, 5, 6, 0].map((d) => ({ d, label: t('backup.wd.' + d) })))
function label(k: string) { return te('backup.f.' + k) ? t('backup.f.' + k) : k }
function toggleDay(d: number) {
  const s = new Set<number>(schedule.value.weekdays || [])
  s.has(d) ? s.delete(d) : s.add(d)
  schedule.value.weekdays = [...s].sort()
}

function body() {
  const sch: any = { kind: schedule.value.kind }
  if (sch.kind === 'hourly') sch.every_hours = Number(schedule.value.every_hours) || 6
  if (sch.kind === 'daily' || sch.kind === 'weekly') sch.at = schedule.value.at || '03:00'
  if (sch.kind === 'weekly') sch.weekdays = schedule.value.weekdays?.length ? schedule.value.weekdays : [0]
  const cfg: Record<string, any> = { ...config.value }
  for (const f of fields.value) if (f.type === 'number') cfg[f.key] = Number(cfg[f.key]) || 0
  const out: any = { name: name.value.trim(), kind: kind.value, config: cfg, mode: mode.value, schedule: sch,
    retention: { ...retention.value, keep_last: +retention.value.keep_last, daily: +retention.value.daily, weekly: +retention.value.weekly, monthly: +retention.value.monthly, volume_mb: +retention.value.volume_mb },
    encryption: { enabled: encEnabled.value, password: encPassword.value || (hasPassword.value ? MASK : '') }, enabled: enabled.value }
  return out
}

async function runTest() {
  testing.value = true
  test.value = null
  try { test.value = await api.post('/api/admin/backup/test', { id: props.target?.id || '', kind: kind.value, config: body().config }) } catch (e) { test.value = { ok: false, error: errorText(e) } } finally { testing.value = false }
}
function trustHostKey() {
  config.value.host_key_sha256 = test.value.host_key
  runTest()
}

async function save() {
  if (encEnabled.value && !hasPassword.value && encPassword.value.length < 8) { toast.error(t('backup.passwordShort')); return }
  busy.value = true
  try {
    const r = props.target ? await api.put(`/api/admin/backup/targets/${props.target.id}`, body()) : await api.post('/api/admin/backup/targets', body())
    emit('saved', r)
    open.value = false
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <Modal v-model:open="open" :title="target ? t('backup.editTarget') : t('backup.newTarget')" :width="600">
    <div class="ed">
      <label class="fld"><span class="lb">{{ t('common.name') }}</span><input v-model="name" class="in" :placeholder="t('backup.namePh')" /></label>
      <div class="fld">
        <span class="lb">{{ t('backup.kind') }}</span>
        <div class="kinds">
          <button v-for="k in KIND_ORDER" :key="k" class="kd" :class="{ on: kind === k }" :disabled="!!target && target.kind !== k" @click="setKind(k)">{{ t('backup.kinds.' + k) }}</button>
        </div>
      </div>
      <div class="grid">
        <template v-for="f in fields" :key="f.key">
          <label v-if="f.type === 'switch'" class="fld sw"><Switch v-model="config[f.key]" /><span>{{ label(f.key) }}</span><InfoTip v-if="f.tip" :text="t('backup.tip.' + f.key)" /></label>
          <label v-else class="fld" :class="{ wide: f.type === 'textarea' || f.key === 'path' || f.key === 'url' || f.key === 'host_key_sha256' }">
            <span class="lb">{{ label(f.key) }}<span v-if="f.optional" class="opt">{{ t('common.optional') }}</span><InfoTip v-if="f.tip" :text="t('backup.tip.' + f.key)" /></span>
            <textarea v-if="f.type === 'textarea'" v-model="config[f.key]" class="in ta" rows="3" spellcheck="false" :placeholder="config[f.key] === MASK ? '' : '-----BEGIN OPENSSH PRIVATE KEY-----'" />
            <input v-else v-model="config[f.key]" class="in" :class="{ mono: f.key === 'host_key_sha256' }" :type="f.type === 'password' ? 'password' : f.type === 'number' ? 'number' : 'text'" :placeholder="f.ph || ''" autocomplete="off" />
          </label>
        </template>
      </div>
      <div class="test">
        <Btn size="sm" icon="refresh" :loading="testing" @click="runTest">{{ t('backup.test') }}</Btn>
        <template v-if="test">
          <span v-if="test.ok" class="ok"><Icon name="check" :size="14" />{{ t('backup.testOk') }}</span>
          <span v-else-if="test.host_key" class="warn"><Icon name="warning" :size="14" />{{ t('backup.hostKey') }} <code>{{ test.host_key }}</code><Btn size="sm" variant="ghost" @click="trustHostKey">{{ t('backup.trust') }}</Btn></span>
          <span v-else class="bad"><Icon name="warning" :size="14" />{{ test.error }}</span>
          <span v-if="test.warning" class="warn">{{ test.warning }}</span>
        </template>
      </div>

      <div class="fld">
        <span class="lb">{{ t('backup.mode') }}<InfoTip :text="t('backup.modeTip')" :width="300" /></span>
        <Segmented v-model="mode" :options="modes" />
      </div>
      <div class="row">
        <label class="fld"><span class="lb">{{ t('backup.schedule') }}</span>
          <select v-model="schedule.kind" class="in">
            <option value="manual">{{ t('backup.sch.manual') }}</option><option value="hourly">{{ t('backup.sch.hourly') }}</option>
            <option value="daily">{{ t('backup.sch.daily') }}</option><option value="weekly">{{ t('backup.sch.weekly') }}</option>
          </select>
        </label>
        <label v-if="schedule.kind === 'hourly'" class="fld"><span class="lb">{{ t('backup.everyHours') }}</span><input v-model="schedule.every_hours" class="in" type="number" min="1" max="168" /></label>
        <label v-if="schedule.kind === 'daily' || schedule.kind === 'weekly'" class="fld"><span class="lb">{{ t('backup.at') }}</span><input v-model="schedule.at" class="in" type="time" /></label>
      </div>
      <div v-if="schedule.kind === 'weekly'" class="days">
        <button v-for="w in weekdays" :key="w.d" class="kd" :class="{ on: schedule.weekdays?.includes(w.d) }" @click="toggleDay(w.d)">{{ w.label }}</button>
      </div>
      <template v-if="mode === 'full'">
        <div class="fld">
          <span class="lb">{{ t('backup.retention') }}</span>
          <Segmented v-model="retention.kind" :options="retKinds" />
        </div>
        <div class="row">
          <label v-if="retention.kind === 'last'" class="fld"><span class="lb">{{ t('backup.keepCount') }}</span><input v-model="retention.keep_last" class="in" type="number" min="1" /></label>
          <template v-else>
            <label class="fld"><span class="lb">{{ t('backup.daily') }}</span><input v-model="retention.daily" class="in" type="number" min="0" /></label>
            <label class="fld"><span class="lb">{{ t('backup.weekly') }}</span><input v-model="retention.weekly" class="in" type="number" min="0" /></label>
            <label class="fld"><span class="lb">{{ t('backup.monthly') }}</span><input v-model="retention.monthly" class="in" type="number" min="0" /></label>
          </template>
          <label class="fld"><span class="lb">{{ t('backup.volume') }}<InfoTip :text="t('backup.volumeTip')" /></span><input v-model="retention.volume_mb" class="in" type="number" min="16" max="4096" /></label>
        </div>
      </template>
      <div class="enc">
        <label class="fld sw"><Switch v-model="encEnabled" /><span>{{ t('backup.encrypt') }}</span><InfoTip :text="t('backup.encryptTip')" :width="300" /></label>
        <input v-if="encEnabled" v-model="encPassword" class="in" type="password" autocomplete="new-password" :placeholder="hasPassword ? t('backup.passwordKeep') : t('backup.passwordPh')" />
      </div>
      <label class="fld sw"><Switch v-model="enabled" /><span>{{ t('backup.enabled') }}</span></label>
    </div>
    <template #footer>
      <Btn @click="open = false">{{ t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="busy" :disabled="!name.trim()" @click="save">{{ t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.ed { display: flex; flex-direction: column; gap: 14px; }
.fld { display: flex; flex-direction: column; gap: 5px; min-width: 0; }
.fld.sw { flex-direction: row; align-items: center; gap: 8px; font-size: 13.5px; }
.lb { font-size: 12.5px; font-weight: 500; color: var(--text-2); display: flex; align-items: center; gap: 4px; }
.opt { color: var(--text-3); font-weight: 400; margin-left: 4px; }
.in { height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); color: var(--text); width: 100%; font: inherit; font-size: 13.5px; }
.in.ta { height: auto; padding: 6px 10px; font-family: var(--font-mono); font-size: 12px; resize: vertical; }
.in.mono { font-family: var(--font-mono); font-size: 12px; }
.in:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.kinds, .days { display: flex; flex-wrap: wrap; gap: 6px; }
.kd { height: 30px; padding: 0 12px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); color: var(--text-2); font-size: 13px; }
.kd:hover:not(:disabled) { background: var(--bg-hover); }
.kd.on { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); }
.kd:disabled { opacity: .4; cursor: not-allowed; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 12px; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-m); background: var(--bg-soft); }
.grid .wide { grid-column: 1 / -1; }
.row { display: flex; gap: 12px; }
.row .fld { flex: 1; }
.test { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; font-size: 13px; }
.ok { color: var(--success); display: inline-flex; align-items: center; gap: 4px; }
.bad { color: var(--danger); display: inline-flex; align-items: center; gap: 4px; word-break: break-all; }
.warn { color: var(--warning); display: inline-flex; align-items: center; gap: 4px; flex-wrap: wrap; }
.warn code { font-family: var(--font-mono); font-size: 11.5px; word-break: break-all; }
.enc { display: flex; flex-direction: column; gap: 8px; }
@media (max-width: 760px) { .grid { grid-template-columns: 1fr; } .row { flex-direction: column; } }
</style>
