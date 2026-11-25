<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import IconBtn from '@/components/ui/IconBtn.vue'
import type { DB } from './useDatabase'
import type { DbRow } from '@/lib/db'

const props = defineProps<{ db: DB; rows: DbRow[] }>()
const { t, locale } = useI18n()
const router = useRouter()
const cursor = ref(new Date())
const dp = computed(() => props.db.prop(props.db.view.value?.config.date_prop || '') || props.db.props.value.find((p) => p.type === 'date'))
const pad = (n: number) => String(n).padStart(2, '0')
const key = (d: Date) => `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
const days = computed(() => {
  const first = new Date(cursor.value.getFullYear(), cursor.value.getMonth(), 1)
  const start = new Date(first)
  start.setDate(1 - ((first.getDay() + 6) % 7))
  return Array.from({ length: 42 }, (_, i) => { const d = new Date(start); d.setDate(start.getDate() + i); return d })
})
function dateOf(r: DbRow): string {
  const p = dp.value
  if (!p) return ''
  if (p.type === 'created_time') return key(new Date(r.created_at))
  if (p.type === 'last_edited_time') return key(new Date(r.updated_at))
  return (r.props?.[p.id]?.start || '').slice(0, 10)
}
const byDay = computed(() => {
  const m: Record<string, DbRow[]> = {}
  for (const r of props.rows) { const k = dateOf(r); if (k) (m[k] ||= []).push(r) }
  return m
})
const title = computed(() => cursor.value.toLocaleDateString(locale.value, { year: 'numeric', month: 'long' }))
const weekNames = computed(() => (locale.value === 'en' ? ['Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat', 'Sun'] : ['一', '二', '三', '四', '五', '六', '日']))
function shift(n: number) { cursor.value = new Date(cursor.value.getFullYear(), cursor.value.getMonth() + n, 1) }
async function add(d: Date) {
  if (!props.db.canEdit.value || !dp.value || dp.value.type !== 'date') return
  await props.db.addRow({ [dp.value.id]: { start: key(d) } })
}
const today = key(new Date())
</script>

<template>
  <div class="cal">
    <div class="ch">
      <span class="ttl">{{ title }}</span><span class="ln-spacer" />
      <IconBtn icon="chevron-left" :label="t('db.prev')" @click="shift(-1)" />
      <button class="today" @click="cursor = new Date()">{{ t('db.today') }}</button>
      <IconBtn icon="chevron-right" :label="t('db.next')" @click="shift(1)" />
    </div>
    <p v-if="!dp" class="hint">{{ t('db.needDateProp') }}</p>
    <div class="grid">
      <div v-for="w in weekNames" :key="w" class="wd">{{ w }}</div>
      <div v-for="d in days" :key="key(d)" class="day" :class="{ other: d.getMonth() !== cursor.getMonth(), today: key(d) === today }" @dblclick="add(d)">
        <div class="dn">{{ d.getDate() }}</div>
        <div v-for="r in byDay[key(d)] || []" :key="r.id" class="ev" @click="router.push(`/p/${r.id}`)">{{ r.title || t('common.untitled') }}</div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.ch { display: flex; align-items: center; gap: 4px; padding: 4px 0 8px; }
.ttl { font-weight: 600; }
.today { border: 1px solid var(--border-strong); background: var(--bg); border-radius: 5px; font-size: 12.5px; padding: 2px 8px; }
.grid { display: grid; grid-template-columns: repeat(7, 1fr); border-left: 1px solid var(--border); border-top: 1px solid var(--border); }
.wd { font-size: 12px; color: var(--text-3); padding: 4px 6px; border-right: 1px solid var(--border); border-bottom: 1px solid var(--border); }
.day { min-height: 96px; border-right: 1px solid var(--border); border-bottom: 1px solid var(--border); padding: 4px; display: flex; flex-direction: column; gap: 3px; min-width: 0; }
.day.other { background: var(--bg-soft); }
.dn { font-size: 12px; color: var(--text-2); text-align: right; }
.today .dn { color: #fff; background: var(--accent); border-radius: 10px; align-self: flex-end; padding: 0 6px; }
.ev { font-size: 12px; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 4px; padding: 2px 5px; cursor: pointer; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; box-shadow: var(--shadow-soft); }
.ev:hover { background: var(--bg-hover); }
.hint { color: var(--text-3); font-size: 13px; }
</style>
