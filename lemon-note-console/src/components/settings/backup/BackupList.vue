<script setup lang="ts">
// 目标上的备份列表：校验、恢复到新目录。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { formatDate } from '@/lib/time'
import { formatSize } from '@/lib/files'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ target: any }>()
const emit = defineEmits<{ run: [id: string] }>()
const { t } = useI18n()
const toast = useToast()
const list = ref<any[] | null>(null)
const err = ref('')
const pw = ref('')
const restoreOf = ref<any>(null)
const to = ref('')
const rpw = ref('')
const busy = ref(false)

async function load(password = '') {
  list.value = null
  err.value = ''
  try {
    const r = password ? await api.post(`/api/admin/backup/targets/${props.target.id}/backups`, { password }) : await api.get(`/api/admin/backup/targets/${props.target.id}/backups`)
    list.value = r.backups || []
  } catch (e) { err.value = errorText(e); list.value = [] }
}
watch(open, (v) => { if (v) { restoreOf.value = null; load() } }, { immediate: true })

async function verify(b: any) {
  try {
    const r = await api.post(`/api/admin/backup/targets/${props.target.id}/verify`, { backup: b.name, password: pw.value })
    emit('run', r.run_id)
  } catch (e) { toast.error(errorText(e)) }
}
async function restore() {
  busy.value = true
  try {
    const r = await api.post(`/api/admin/backup/targets/${props.target.id}/restore`, { backup: restoreOf.value.name, to: to.value.trim(), password: rpw.value || pw.value })
    restoreOf.value = null
    emit('run', r.run_id)
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <Modal v-model:open="open" :title="t('backup.backupsOf', { name: target?.name })" :width="680">
    <div class="bl">
      <div v-if="err" class="err">{{ err }}
        <div class="pwrow"><input v-model="pw" class="in" type="password" :placeholder="t('backup.password')" /><Btn size="sm" @click="load(pw)">{{ t('backup.unlockList') }}</Btn></div>
      </div>
      <div v-if="list === null" class="ld" />
      <div v-else-if="!list.length && !err" class="empty">{{ t('backup.noBackups') }}</div>
      <div v-for="b in list || []" :key="b.name" class="item">
        <Icon :name="b.mode === 'mirror' ? 'refresh' : 'database'" :size="18" class="ic" />
        <div class="info">
          <div class="nm"><span class="ln-ellipsis">{{ b.name }}</span>
            <span class="tag">{{ b.mode === 'mirror' ? t('backup.modeMirror') : t('backup.modeFull') }}</span>
            <span v-if="b.encrypted" class="tag"><Icon name="lock" :size="11" />{{ t('backup.encrypted') }}</span>
            <span v-if="!b.current" class="tag other">{{ t('backup.otherInstance', { id: b.instance }) }}</span>
          </div>
          <div class="sub">{{ b.created_at ? formatDate(b.created_at, true) : '' }}<template v-if="b.size"> · {{ formatSize(b.size) }}</template><template v-if="b.volumes > 1"> · {{ t('backup.volumes', { n: b.volumes }) }}</template><template v-if="b.note"> · {{ b.note }}</template></div>
        </div>
        <Btn size="sm" variant="ghost" @click="verify(b)">{{ t('backup.verify') }}</Btn>
        <Btn size="sm" @click="restoreOf = b; to = ''; rpw = ''">{{ t('backup.restore') }}</Btn>
      </div>
      <div v-if="restoreOf" class="restore">
        <div class="rt">{{ t('backup.restoreTitle', { name: restoreOf.name }) }}<InfoTip :text="t('backup.restoreTip')" :width="320" /></div>
        <label class="fld"><span class="lb">{{ t('backup.restoreTo') }}</span><input v-model="to" class="in mono" :placeholder="t('backup.restoreToPh')" /></label>
        <label v-if="restoreOf.encrypted" class="fld"><span class="lb">{{ t('backup.password') }}</span><input v-model="rpw" class="in" type="password" autocomplete="off" /></label>
        <div class="acts"><Btn size="sm" variant="ghost" @click="restoreOf = null">{{ t('common.cancel') }}</Btn><Btn size="sm" variant="primary" :loading="busy" :disabled="!to.trim()" @click="restore">{{ t('backup.startRestore') }}</Btn></div>
      </div>
    </div>
  </Modal>
</template>

<style scoped>
.bl { display: flex; flex-direction: column; gap: 6px; min-height: 160px; }
.ld { height: 120px; }
.empty { color: var(--text-3); font-size: 13px; text-align: center; padding: 40px 0; }
.err { color: var(--danger); font-size: 13px; display: flex; flex-direction: column; gap: 8px; }
.pwrow { display: flex; gap: 8px; }
.item { display: flex; align-items: center; gap: 10px; padding: 8px 6px; border-radius: var(--radius-m); }
.item:hover { background: var(--bg-hover); }
.ic { color: var(--text-3); flex: none; }
.info { flex: 1; min-width: 0; }
.nm { display: flex; align-items: center; gap: 6px; font-size: 13px; font-family: var(--font-mono); min-width: 0; }
.tag { font-family: var(--font-ui); font-size: 11px; background: var(--bg-hover); color: var(--text-2); border-radius: 4px; padding: 0 5px; display: inline-flex; align-items: center; gap: 3px; white-space: nowrap; }
.tag.other { color: var(--warning); }
.sub { font-size: 12px; color: var(--text-3); }
.restore { margin-top: 8px; border: 1px solid var(--border); border-radius: var(--radius-m); padding: 12px; display: flex; flex-direction: column; gap: 10px; background: var(--bg-soft); }
.rt { font-weight: 500; font-size: 13.5px; display: flex; align-items: center; gap: 4px; word-break: break-all; }
.fld { display: flex; flex-direction: column; gap: 5px; }
.lb { font-size: 12.5px; color: var(--text-2); }
.in { height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); color: var(--text); width: 100%; font: inherit; font-size: 13.5px; }
.in.mono { font-family: var(--font-mono); font-size: 12.5px; }
.acts { display: flex; justify-content: flex-end; gap: 8px; }
</style>
