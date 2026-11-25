<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import TextField from '@/components/ui/TextField.vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { api, setCsrf } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, setLocale } from '@/i18n'

const { t } = useI18n()
const s = useSession()
const toast = useToast()
const name = ref(s.user?.display_name || '')
const email = ref(s.user?.email || '')
const lang = ref(s.user?.locale || 'zh-CN')
const oldPw = ref('')
const newPw = ref('')
const newPw2 = ref('')
const busy = ref(false)

async function save() {
  busy.value = true
  try {
    const r = await api.patch('/api/me', { display_name: name.value, email: email.value, locale: lang.value })
    s.user = r.user
    setLocale(lang.value)
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function changePw() {
  if (newPw.value !== newPw2.value) { toast.error(t('setup.mismatch')); return }
  try {
    const r = await api.post('/api/me/password', { old_password: oldPw.value, new_password: newPw.value })
    setCsrf(r.csrf)
    oldPw.value = newPw.value = newPw2.value = ''
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <Section :title="t('settings.profile')">
    <div class="grid">
      <TextField :model-value="s.user?.username || ''" :label="t('setup.username')" disabled />
      <TextField v-model="name" :label="t('setup.displayName')" />
      <TextField v-model="email" label="Email" />
      <div class="field"><span class="lbl">{{ t('settings.language') }}</span><Segmented v-model="lang" :options="[{ value: 'zh-CN', label: '中文' }, { value: 'en', label: 'English' }]" /></div>
    </div>
    <div><Btn variant="primary" :loading="busy" @click="save">{{ t('common.save') }}</Btn></div>
  </Section>
  <Section :title="t('settings.changePassword')">
    <div class="grid">
      <TextField v-model="oldPw" :label="t('settings.oldPassword')" type="password" autocomplete="current-password" />
      <span />
      <TextField v-model="newPw" :label="t('settings.newPassword')" type="password" autocomplete="new-password" />
      <TextField v-model="newPw2" :label="t('setup.password2')" type="password" autocomplete="new-password" />
    </div>
    <div><Btn :disabled="!oldPw || !newPw" @click="changePw">{{ t('settings.changePassword') }}</Btn></div>
  </Section>
</template>

<style scoped>
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.lbl { font-size: 12.5px; font-weight: 500; color: var(--text-2); }
@media (max-width: 760px) { .grid { grid-template-columns: 1fr; } }
</style>
