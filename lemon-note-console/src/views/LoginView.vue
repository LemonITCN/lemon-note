<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from './AuthFrame.vue'
import TextField from '@/components/ui/TextField.vue'
import Btn from '@/components/ui/Btn.vue'
import { useSession } from '@/stores/session'
import { api, setCsrf } from '@/api/client'
import { errorText } from '@/i18n'

const props = defineProps<{ register?: boolean }>()
const { t } = useI18n()
const s = useSession()
const route = useRoute()
const router = useRouter()
const username = ref('')
const password = ref('')
const password2 = ref('')
const displayName = ref('')
const code = ref('')
const error = ref('')
const busy = ref(false)
const step = computed(() => (s.mfaPending ? 'mfa' : props.register ? 'register' : 'login'))

function next() {
  const n = route.query.next
  router.replace(typeof n === 'string' && n.startsWith('/') && !n.startsWith('//') ? n : '/')
}

async function submit() {
  error.value = ''
  busy.value = true
  try {
    if (step.value === 'mfa') {
      await s.verifyMFA(code.value)
      next()
    } else if (step.value === 'register') {
      if (password.value !== password2.value) throw new Error(t('setup.mismatch'))
      const r = await api.post<any>('/api/auth/register', { username: username.value, display_name: displayName.value, password: password.value, locale: navigator.language.startsWith('zh') ? 'zh-CN' : 'en' })
      setCsrf(r.csrf)
      s.adoptUser(r.user)
      next()
    } else {
      const r = await s.login(username.value, password.value)
      if (r === 'ok') next()
    }
  } catch (e) {
    error.value = errorText(e)
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <AuthFrame :title="step === 'mfa' ? t('login.mfaTitle') : step === 'register' ? t('login.register') : (s.boot?.site_name || t('login.title'))"
    :subtitle="step === 'mfa' ? t('login.mfaHint') : undefined">
    <form class="form" @submit.prevent="submit">
      <template v-if="step === 'mfa'">
        <TextField v-model="code" autocomplete="one-time-code" autofocus mono placeholder="000000" />
      </template>
      <template v-else>
        <TextField v-model="username" :label="t('login.username')" autocomplete="username" autofocus />
        <TextField v-if="step === 'register'" v-model="displayName" :label="t('setup.displayName')" />
        <TextField v-model="password" :label="t('login.password')" type="password" :autocomplete="step === 'register' ? 'new-password' : 'current-password'" />
        <TextField v-if="step === 'register'" v-model="password2" :label="t('setup.password2')" type="password" autocomplete="new-password" />
      </template>
      <p v-if="error" class="err">{{ error }}</p>
      <Btn variant="primary" type="submit" block :loading="busy">{{ step === 'mfa' ? t('login.verify') : step === 'register' ? t('common.create') : t('login.submit') }}</Btn>
      <RouterLink v-if="step === 'login' && s.boot?.allow_registration" class="alt" to="/register">{{ t('login.register') }}</RouterLink>
      <RouterLink v-if="step === 'register'" class="alt" to="/login">{{ t('login.haveAccount') }}</RouterLink>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 14px; margin-top: 6px; }
.err { color: var(--danger); margin: -4px 0 0; font-size: 13px; }
.alt { text-align: center; color: var(--text-2); font-size: 13px; text-decoration: none; }
.alt:hover { color: var(--text); }
</style>
