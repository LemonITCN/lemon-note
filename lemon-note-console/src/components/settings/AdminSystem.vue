<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Row from './Row.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const { t } = useI18n()
const toast = useToast()
const s = ref<any>(null)
onMounted(async () => { try { s.value = (await api.get('/api/admin/settings')).settings } catch (e) { toast.error(errorText(e)) } })
async function save() {
  try { s.value = (await api.put('/api/admin/settings', s.value)).settings; toast.success(t('common.saved')) } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <template v-if="s">
    <Section :title="t('settings.system')">
      <TextField v-model="s.site_name" :label="t('setup.siteName')" />
      <Row :label="t('settings.allowRegistration')" :tip="t('settings.allowRegistrationTip')"><Switch v-model="s.allow_registration" /></Row>
      <Row :label="t('settings.requireMfa')" :tip="t('settings.requireMfaTip')"><Switch v-model="s.require_mfa" /></Row>
      <Row :label="t('settings.publicSharing')" :tip="t('settings.publicSharingTip')"><Switch v-model="s.public_sharing" /></Row>
      <Row :label="t('settings.sessionIdle')"><input v-model.number="s.session_idle_days" class="num" type="number" min="1" /></Row>
      <Row :label="t('settings.sessionMax')"><input v-model.number="s.session_max_days" class="num" type="number" min="1" /></Row>
      <Row :label="t('settings.maxUpload')"><input v-model.number="s.max_upload_mb" class="num" type="number" min="1" /></Row>
      <Row :label="t('settings.trashRetain')"><input v-model.number="s.trash_retain_days" class="num" type="number" min="1" /></Row>
      <Row :label="t('settings.historyRetain')"><input v-model.number="s.history_retain_days" class="num" type="number" min="1" /></Row>
    </Section>
    <Section :title="t('settings.webSearch')" :tip="t('settings.webSearchTip')">
      <Row :label="t('settings.searchProvider')">
        <select v-model="s.web_search.provider" class="sel"><option value="">{{ t('common.none') }}</option><option value="searxng">SearXNG</option><option value="tavily">Tavily</option><option value="bocha">{{ t('settings.bocha') }}</option></select>
      </Row>
      <TextField v-if="s.web_search.provider === 'searxng'" v-model="s.web_search.url" label="SearXNG URL" placeholder="http://192.168.1.10:8080" />
      <TextField v-if="s.web_search.provider === 'tavily' || s.web_search.provider === 'bocha'" v-model="s.web_search.api_key" label="API Key" type="password" autocomplete="off" />
    </Section>
    <div><Btn variant="primary" @click="save">{{ t('common.save') }}</Btn></div>
  </template>
</template>

<style scoped>
.num { width: 90px; height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); text-align: right; }
.sel { height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); color: var(--text); }
</style>
