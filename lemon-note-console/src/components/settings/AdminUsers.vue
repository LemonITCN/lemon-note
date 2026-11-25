<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Modal from '@/components/ui/Modal.vue'
import Menu from '@/components/ui/Menu.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatRelative } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'

const { t } = useI18n()
const s = useSession()
const toast = useToast()
const users = ref<any[]>([])
const open = ref(false)
const form = ref({ username: '', display_name: '', password: '', role: 'member' })

async function load() { try { users.value = (await api.get('/api/admin/users')).users } catch (e) { toast.error(errorText(e)) } }
onMounted(load)

async function create() {
  try { await api.post('/api/admin/users', form.value); open.value = false; form.value = { username: '', display_name: '', password: '', role: 'member' }; await load() } catch (e) { toast.error(errorText(e)) }
}
async function patch(id: string, body: any) { try { await api.patch(`/api/admin/users/${id}`, body); await load() } catch (e) { toast.error(errorText(e)) } }
async function resetPw(u: any) {
  const pw = window.prompt(t('settings.newPassword') + ' · ' + u.username)
  if (!pw) return
  try { await api.post(`/api/admin/users/${u.id}/password`, { password: pw }); toast.success(t('common.saved')) } catch (e) { toast.error(errorText(e)) }
}
async function reset2fa(u: any) {
  if (!(await confirmDialog({ title: t('settings.reset2fa'), message: u.username, danger: true }))) return
  try { await api.post(`/api/admin/users/${u.id}/reset-2fa`); await load() } catch (e) { toast.error(errorText(e)) }
}
function menu(u: any) {
  const me = u.id === s.user?.id
  return [
    { label: t('settings.resetPassword'), icon: 'key', run: () => resetPw(u) },
    { label: t('settings.reset2fa'), icon: 'shield', disabled: !u.totp_enabled, run: () => reset2fa(u) },
    { label: u.role === 'admin' ? t('settings.makeMember') : t('settings.makeAdmin'), icon: 'user', disabled: me, run: () => patch(u.id, { role: u.role === 'admin' ? 'member' : 'admin' }) },
    { separator: true },
    { label: u.status === 'active' ? t('settings.disableUser') : t('settings.enableUser'), icon: u.status === 'active' ? 'lock' : 'unlock', danger: u.status === 'active', disabled: me, run: () => patch(u.id, { status: u.status === 'active' ? 'disabled' : 'active' }) },
  ]
}
</script>

<template>
  <Section :title="t('settings.users')" :tip="t('settings.usersTip')">
    <div v-for="u in users" :key="u.id" class="u" :class="{ off: u.status !== 'active' }">
      <div class="ui"><div class="un">{{ u.display_name }} <span class="uu">@{{ u.username }}</span><span v-if="u.role === 'admin'" class="b">{{ t('roles.admin') }}</span><span v-if="u.totp_enabled" class="b">2FA</span><span v-if="u.status !== 'active'" class="b off">{{ t('common.disabled') }}</span></div>
        <div class="um">{{ u.last_login_at ? formatRelative(u.last_login_at) : t('settings.neverUsed') }}</div></div>
      <Menu :items="menu(u)" align="end"><IconBtn icon="more" :label="t('common.more')" /></Menu>
    </div>
    <div><Btn size="sm" variant="primary" icon="plus" @click="open = true">{{ t('settings.newUser') }}</Btn></div>
  </Section>
  <Modal v-model:open="open" :title="t('settings.newUser')" :width="420">
    <div class="form">
      <TextField v-model="form.username" :label="t('setup.username')" autofocus />
      <TextField v-model="form.display_name" :label="t('setup.displayName')" />
      <TextField v-model="form.password" :label="t('setup.password')" type="password" autocomplete="new-password" />
      <label class="chk"><input type="checkbox" :checked="form.role === 'admin'" @change="form.role = ($event.target as HTMLInputElement).checked ? 'admin' : 'member'" />{{ t('settings.admin') }}</label>
    </div>
    <template #footer><Btn @click="open = false">{{ t('common.cancel') }}</Btn><Btn variant="primary" @click="create">{{ t('common.create') }}</Btn></template>
  </Modal>
</template>

<style scoped>
.u { display: flex; align-items: center; gap: 10px; }
.u.off { opacity: .6; }
.ui { flex: 1; min-width: 0; }
.un { font-size: 13.5px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.uu { color: var(--text-3); font-size: 12px; }
.b { font-size: 11px; background: var(--bg-hover); color: var(--text-2); border-radius: 4px; padding: 0 5px; }
.b.off { color: var(--danger); }
.um { font-size: 12px; color: var(--text-3); }
.form { display: flex; flex-direction: column; gap: 12px; }
.chk { display: flex; gap: 8px; align-items: center; font-size: 13.5px; }
</style>
