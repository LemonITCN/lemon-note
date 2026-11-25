<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import Section from './Section.vue'
import Row from './Row.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Modal from '@/components/ui/Modal.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatRelative } from '@/lib/time'

const { t } = useI18n()
const s = useSession()
const toast = useToast()
const sessions = ref<any[]>([])
const step = ref<'' | 'password' | 'scan' | 'codes' | 'disable' | 'regen'>('')
const pw = ref('')
const code = ref('')
const qr = ref('')
const secret = ref('')
const codes = ref<string[]>([])
const busy = ref(false)

async function loadSessions() { try { sessions.value = (await api.get('/api/me/sessions')).sessions } catch {} }
onMounted(loadSessions)

async function startSetup() {
  busy.value = true
  try {
    const r = await api.post('/api/me/totp/setup', { password: pw.value })
    secret.value = r.secret
    qr.value = await QRCode.toDataURL(r.url, { margin: 1, width: 200 })
    step.value = 'scan'
    pw.value = ''
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function enable() {
  busy.value = true
  try {
    const r = await api.post('/api/me/totp/enable', { code: code.value })
    codes.value = r.recovery_codes
    step.value = 'codes'
    code.value = ''
    if (s.user) s.user.totp_enabled = true
    s.mfaSetupRequired = false
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function disable() {
  busy.value = true
  try {
    await api.post('/api/me/totp/disable', { password: pw.value, code: code.value })
    if (s.user) s.user.totp_enabled = false
    step.value = ''
    pw.value = code.value = ''
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function regen() {
  busy.value = true
  try {
    codes.value = (await api.post('/api/me/recovery-codes', { password: pw.value })).recovery_codes
    step.value = 'codes'
    pw.value = ''
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function revoke(id: string) { try { await api.del(`/api/me/sessions/${id}`); await loadSessions() } catch (e) { toast.error(errorText(e)) } }
async function revokeOthers() { try { await api.post('/api/me/sessions/revoke-others'); await loadSessions() } catch (e) { toast.error(errorText(e)) } }

function uaLabel(ua: string) {
  const b = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : 'Browser'
  const o = /Windows/.test(ua) ? 'Windows' : /Mac OS X/.test(ua) ? (/iPhone|iPad/.test(ua) ? 'iOS' : 'macOS') : /Android/.test(ua) ? 'Android' : /Linux/.test(ua) ? 'Linux' : ''
  return o ? `${b} · ${o}` : b
}
function copyCodes() { navigator.clipboard?.writeText(codes.value.join('\n')); toast.success(t('common.copied')) }
</script>

<template>
  <Section :title="t('settings.twoFactor')" :tip="t('settings.twoFactorTip')">
    <Row :label="s.user?.totp_enabled ? t('settings.twoFactorOn') : t('settings.twoFactorOff')">
      <template v-if="s.user?.totp_enabled">
        <Btn size="sm" @click="step = 'regen'">{{ t('settings.recoveryCodes') }}</Btn>
        <Btn size="sm" variant="ghost" @click="step = 'disable'">{{ t('settings.disable') }}</Btn>
      </template>
      <Btn v-else size="sm" variant="primary" @click="step = 'password'">{{ t('settings.enable') }}</Btn>
    </Row>
  </Section>
  <Section :title="t('settings.sessions')">
    <div v-for="x in sessions" :key="x.id" class="sess">
      <div class="si"><div class="sn">{{ uaLabel(x.ua) }}<span v-if="x.current" class="cur">{{ t('settings.current') }}</span></div><div class="sm">{{ x.ip }} · {{ formatRelative(x.last_seen_at) }}</div></div>
      <Btn v-if="!x.current" size="sm" variant="ghost" @click="revoke(x.id)">{{ t('settings.revoke') }}</Btn>
    </div>
    <div><Btn size="sm" @click="revokeOthers">{{ t('settings.revokeOthers') }}</Btn></div>
  </Section>

  <Modal :open="step !== ''" :title="t('settings.twoFactor')" :width="420" @update:open="(v: boolean) => !v && (step = '')">
    <div class="flow">
      <template v-if="step === 'password' || step === 'regen'">
        <TextField v-model="pw" :label="t('settings.oldPassword')" type="password" autocomplete="current-password" autofocus />
        <Btn variant="primary" :loading="busy" :disabled="!pw" @click="step === 'regen' ? regen() : startSetup()">{{ t('common.next') }}</Btn>
      </template>
      <template v-else-if="step === 'scan'">
        <p class="p">{{ t('settings.scanQr') }}</p>
        <img :src="qr" alt="" class="qr" />
        <code class="secret">{{ secret }}</code>
        <TextField v-model="code" :label="t('settings.enterCode')" mono autocomplete="one-time-code" />
        <Btn variant="primary" :loading="busy" :disabled="code.length < 6" @click="enable">{{ t('settings.enable') }}</Btn>
      </template>
      <template v-else-if="step === 'codes'">
        <p class="p">{{ t('settings.recoveryTip') }}</p>
        <div class="codes"><code v-for="c in codes" :key="c">{{ c }}</code></div>
        <Btn icon="copy" @click="copyCodes">{{ t('common.copy') }}</Btn>
        <Btn variant="primary" @click="step = ''">{{ t('common.done') }}</Btn>
      </template>
      <template v-else-if="step === 'disable'">
        <TextField v-model="pw" :label="t('settings.oldPassword')" type="password" />
        <TextField v-model="code" :label="t('settings.enterCode')" mono />
        <Btn variant="danger" :loading="busy" :disabled="!pw || !code" @click="disable">{{ t('settings.disable') }}</Btn>
      </template>
    </div>
  </Modal>
</template>

<style scoped>
.sess { display: flex; align-items: center; gap: 10px; padding: 6px 0; }
.si { flex: 1; min-width: 0; }
.sn { font-size: 13.5px; display: flex; align-items: center; gap: 6px; }
.sm { font-size: 12px; color: var(--text-3); }
.cur { font-size: 11px; color: var(--accent); background: var(--accent-soft); border-radius: 4px; padding: 0 5px; }
.flow { display: flex; flex-direction: column; gap: 12px; }
.p { margin: 0; color: var(--text-2); font-size: 13px; line-height: 1.6; }
.qr { width: 200px; height: 200px; align-self: center; border-radius: 8px; background: #fff; padding: 6px; }
.secret { font-family: var(--font-mono); font-size: 12px; text-align: center; color: var(--text-2); word-break: break-all; }
.codes { display: grid; grid-template-columns: 1fr 1fr; gap: 6px; }
.codes code { font-family: var(--font-mono); font-size: 13px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: 5px; padding: 4px 8px; text-align: center; }
</style>
