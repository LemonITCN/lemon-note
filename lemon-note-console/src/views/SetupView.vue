<script setup lang="ts">
import { ref, computed } from 'vue'
import { useRoute } from 'vue-router'
import { useI18n } from 'vue-i18n'
import AuthFrame from './AuthFrame.vue'
import TextField from '@/components/ui/TextField.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { useSession } from '@/stores/session'
import { api } from '@/api/client'
import { errorText, setLocale } from '@/i18n'
import { useToast } from '@/stores/toast'

const { t, locale } = useI18n()
const s = useSession()
const route = useRoute()
const toast = useToast()
const token = ref(typeof route.query.token === 'string' ? route.query.token : '')
const engine = ref('sqlite')
const dsn = ref('')
const username = ref('admin')
const displayName = ref('')
const password = ref('')
const password2 = ref('')
const siteName = ref('Lemon Note')
const lang = ref(locale.value as string)
const error = ref('')
const busy = ref(false)
const testing = ref(false)
const dbOk = ref(false)

const dsnPlaceholder = computed(() => engine.value === 'mysql' ? 'user:password@tcp(127.0.0.1:3306)/lemon_note' : 'postgres://user:password@127.0.0.1:5432/lemon_note')
const engines = computed(() => [
  { value: 'sqlite', label: t('setup.sqlite') },
  { value: 'mysql', label: 'MySQL' },
  { value: 'postgres', label: 'PostgreSQL' },
])

function changeLang(v: string) { lang.value = v; setLocale(v) }

async function testDb() {
  testing.value = true
  error.value = ''
  dbOk.value = false
  try {
    await api.post('/api/setup/test-db', { token: token.value, engine: engine.value, dsn: dsn.value })
    dbOk.value = true
  } catch (e) { error.value = errorText(e) } finally { testing.value = false }
}

async function finish() {
  error.value = ''
  if (password.value !== password2.value) { error.value = t('setup.mismatch'); return }
  busy.value = true
  try {
    await api.post('/api/setup', { token: token.value, engine: engine.value, dsn: dsn.value, username: username.value, display_name: displayName.value,
      password: password.value, locale: lang.value, site_name: siteName.value })
    toast.success(t('setup.done'))
    location.href = '/login'
  } catch (e) { error.value = errorText(e) } finally { busy.value = false }
}
</script>

<template>
  <AuthFrame :title="t('setup.title')" :subtitle="t('setup.subtitle')" wide>
    <form class="form" @submit.prevent="finish">
      <div class="row-between">
        <span class="label">{{ t('setup.language') }}</span>
        <Segmented :model-value="lang" :options="[{ value: 'zh-CN', label: '中文' }, { value: 'en', label: 'English' }]" @update:model-value="changeLang" />
      </div>
      <TextField v-model="token" mono autocomplete="off">
        <template #label>{{ t('setup.token') }}<InfoTip :text="t('setup.tokenTip')" /></template>
      </TextField>
      <div class="block">
        <span class="label">{{ t('setup.dataDir') }}<InfoTip :text="t('setup.dataDirTip')" /></span>
        <code class="path">{{ s.boot?.data_dir }}</code>
      </div>
      <div class="block">
        <span class="label">{{ t('setup.engine') }}<InfoTip :text="t('setup.engineTip')" /></span>
        <Segmented v-model="engine" :options="engines" />
        <template v-if="engine !== 'sqlite'">
          <div class="dsn">
            <TextField v-model="dsn" mono :placeholder="dsnPlaceholder" autocomplete="off" />
            <Btn :loading="testing" @click="testDb">{{ dbOk ? t('setup.dbOk') : t('setup.testDb') }}</Btn>
          </div>
        </template>
      </div>
      <div class="sep">{{ t('setup.admin') }}</div>
      <div class="grid">
        <TextField v-model="username" :label="t('setup.username')" autocomplete="username" />
        <TextField v-model="displayName" :label="t('setup.displayName')" />
        <TextField v-model="password" :label="t('setup.password')" type="password" autocomplete="new-password" />
        <TextField v-model="password2" :label="t('setup.password2')" type="password" autocomplete="new-password" />
      </div>
      <TextField v-model="siteName" :label="t('setup.siteName')" />
      <p v-if="error" class="err">{{ error }}</p>
      <Btn variant="primary" type="submit" block :loading="busy">{{ t('setup.finish') }}</Btn>
    </form>
  </AuthFrame>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 16px; }
.label { font-size: 12.5px; font-weight: 500; color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.block { display: flex; flex-direction: column; gap: 8px; }
.row-between { display: flex; align-items: center; justify-content: space-between; }
.path { font-family: var(--font-mono); font-size: 12.5px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; word-break: break-all; }
.dsn { display: flex; gap: 8px; align-items: flex-end; }
.dsn > :first-child { flex: 1; }
.sep { font-size: 12px; color: var(--text-3); border-top: 1px solid var(--border); padding-top: 14px; margin-top: 2px; font-weight: 500; }
.grid { display: grid; grid-template-columns: 1fr 1fr; gap: 12px; }
.err { color: var(--danger); margin: 0; font-size: 13px; }
@media (max-width: 760px) { .grid { grid-template-columns: 1fr; } }
</style>
