<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Btn from '@/components/ui/Btn.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const { t } = useI18n()
const toast = useToast()
const text = ref('')
onMounted(async () => { try { text.value = (await api.get('/api/agent/instructions')).instructions || '' } catch {} })
async function save() {
  try { await api.put('/api/agent/instructions', { instructions: text.value }); toast.success(t('common.saved')) } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <Section :title="t('settings.agentInstructions')" :tip="t('settings.agentInstructionsTip')">
    <textarea v-model="text" class="ta" rows="10" :placeholder="t('settings.agentInstructionsPh')" />
    <div><Btn variant="primary" @click="save">{{ t('common.save') }}</Btn></div>
  </Section>
</template>

<style scoped>
.ta { width: 100%; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 10px; background: var(--bg); outline: none; font-size: 13.5px; line-height: 1.6; resize: vertical; }
.ta:focus { border-color: var(--accent); }
</style>
