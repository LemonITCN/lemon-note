<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Row from './Row.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useAgent } from '@/stores/agent'
import { errorText } from '@/i18n'

const { t } = useI18n()
const toast = useToast()
const agent = useAgent()
const data = ref<any>(null)
const key = ref('')
const enabled = ref(true)
const def = ref('')
const busy = ref(false)
const chatModels = computed(() => (data.value?.models || []).filter((m: any) => m.type === 'chat'))

async function load() {
  try {
    data.value = await api.get('/api/admin/ai')
    enabled.value = data.value.provider.enabled ?? true
    def.value = data.value.default_model || ''
  } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)

async function save() {
  busy.value = true
  try {
    data.value = await api.put('/api/admin/ai', { api_key: key.value, enabled: enabled.value, default_model: def.value })
    key.value = ''
    await agent.loadModels()
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <Section title="OpenCode Go" :tip="t('settings.opencodeTip')">
    <TextField v-model="key" label="API Key" type="password" autocomplete="off" :placeholder="data?.provider.has_key ? t('settings.keySet') : 'oc_…'" />
    <Row :label="t('common.enabled')"><Switch v-model="enabled" /></Row>
    <Row :label="t('settings.defaultModel')">
      <select v-model="def" class="sel"><option value="">{{ t('settings.autoPick') }}</option><option v-for="m in chatModels" :key="m.id" :value="m.id">{{ m.name }}</option></select>
    </Row>
    <p v-if="data?.error" class="err">{{ data.error }}</p>
    <p v-else-if="chatModels.length" class="ok">{{ t('settings.modelsAvailable', { n: chatModels.length }) }}</p>
    <div><Btn variant="primary" :loading="busy" @click="save">{{ t('common.save') }}</Btn></div>
  </Section>
</template>

<style scoped>
.sel { height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); color: var(--text); max-width: 260px; }
.err { color: var(--danger); font-size: 13px; margin: 0; }
.ok { color: var(--success); font-size: 13px; margin: 0; }
</style>
