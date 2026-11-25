<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Row from './Row.vue'
import Btn from '@/components/ui/Btn.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatDate } from '@/lib/time'
import { formatSize } from '@/lib/files'

const { t } = useI18n()
const toast = useToast()
const st = ref<any>(null)
const busy = ref(false)
async function load() { try { st.value = await api.get('/api/admin/status') } catch (e) { toast.error(errorText(e)) } }
onMounted(load)
async function snap() {
  busy.value = true
  try { await api.post('/api/admin/snapshot'); await load(); toast.success(t('common.done')) } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
function uptime(s: number) { const h = Math.floor(s / 3600); const m = Math.floor((s % 3600) / 60); return `${h}h ${m}m` }
</script>

<template>
  <template v-if="st">
    <Section :title="t('settings.status')">
      <Row :label="t('settings.version')"><code>{{ st.version }}</code></Row>
      <Row :label="t('settings.uptime')"><span>{{ uptime(st.uptime_seconds) }}</span></Row>
      <Row :label="t('settings.memory')"><span>{{ formatSize(st.memory_bytes) }}</span></Row>
      <Row :label="t('settings.connections')"><span>{{ st.connections }}</span></Row>
      <Row :label="t('settings.platform')"><span>{{ st.os }}/{{ st.arch }} · {{ st.go }}</span></Row>
    </Section>
    <Section :title="t('settings.storage')" :tip="t('setup.dataDirTip')">
      <Row :label="t('setup.dataDir')"><code class="path">{{ st.persist.data_dir }}</code></Row>
      <Row :label="t('settings.cacheDir')"><code class="path">{{ st.persist.cache_dir }}</code></Row>
      <Row :label="t('setup.engine')"><span>{{ st.persist.engine }}</span></Row>
      <Row :label="t('settings.lastSnapshot')"><span>{{ formatDate(st.persist.snapshot_at, true) }}</span></Row>
      <Row :label="t('settings.journal')"><span>{{ st.persist.journal_files }} · {{ formatSize(st.persist.journal_bytes) }}</span></Row>
      <div v-for="w in st.persist.warnings || []" :key="w" class="warn">{{ w }}</div>
      <div><Btn :loading="busy" @click="snap">{{ t('settings.snapshotNow') }}</Btn></div>
    </Section>
  </template>
</template>

<style scoped>
code { font-family: var(--font-mono); font-size: 12px; }
.path { max-width: 380px; word-break: break-all; text-align: right; display: inline-block; }
.warn { color: var(--warning); font-size: 13px; background: rgba(194, 124, 14, .1); border-radius: var(--radius-m); padding: 8px 10px; }
</style>
