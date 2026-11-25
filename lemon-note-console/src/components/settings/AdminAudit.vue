<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import { api } from '@/api/client'
import { formatDate } from '@/lib/time'
import { userName } from '@/lib/users'

const { t, te } = useI18n()
const logs = ref<any[]>([])
onMounted(async () => { try { logs.value = (await api.get('/api/admin/audit?limit=300')).logs } catch {} })
const label = (a: string) => (te('audit.' + a) ? t('audit.' + a) : a)
</script>

<template>
  <Section :title="t('settings.audit')">
    <table class="tb">
      <tr v-for="l in logs" :key="l.id">
        <td class="tm">{{ formatDate(l.ts, true) }}</td>
        <td>{{ l.user_id ? userName(l.user_id) : '—' }}</td>
        <td :class="{ bad: l.action.includes('failed') }">{{ label(l.action) }}</td>
        <td class="ip">{{ l.ip }}</td>
      </tr>
    </table>
  </Section>
</template>

<style scoped>
.tb { width: 100%; border-collapse: collapse; font-size: 12.5px; }
td { padding: 5px 6px; border-bottom: 1px solid var(--border); }
.tm, .ip { color: var(--text-3); white-space: nowrap; }
.bad { color: var(--danger); }
</style>
