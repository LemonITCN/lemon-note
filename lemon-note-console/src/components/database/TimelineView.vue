<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { DB } from './useDatabase'
import type { DbRow } from '@/lib/db'

const props = defineProps<{ db: DB; rows: DbRow[] }>()
const { t } = useI18n()
const router = useRouter()
const DAY = 86400000
const dp = computed(() => props.db.prop(props.db.view.value?.config.date_prop || '') || props.db.props.value.find((p) => p.type === 'date'))
function range(r: DbRow): [number, number] | null {
  const v = dp.value ? r.props?.[dp.value.id] : null
  if (!v?.start) return null
  const s = new Date(v.start.slice(0, 10)).getTime()
  const e = v.end ? new Date(v.end.slice(0, 10)).getTime() : s
  return [s, Math.max(s, e)]
}
const items = computed(() => props.rows.map((r) => ({ r, rg: range(r) })).filter((x) => x.rg) as { r: DbRow; rg: [number, number] }[])
const bounds = computed(() => {
  if (!items.value.length) { const n = Date.now(); return [n - 7 * DAY, n + 21 * DAY] }
  const min = Math.min(...items.value.map((x) => x.rg[0])) - 3 * DAY
  const max = Math.max(...items.value.map((x) => x.rg[1])) + 4 * DAY
  return [min, max]
})
const days = computed(() => Math.round((bounds.value[1] - bounds.value[0]) / DAY))
const COL = 32
const pos = (t0: number) => Math.round((t0 - bounds.value[0]) / DAY) * COL
const ticks = computed(() => Array.from({ length: days.value }, (_, i) => new Date(bounds.value[0] + i * DAY)))
const todayX = computed(() => pos(new Date(new Date().toDateString()).getTime()))
</script>

<template>
  <div class="tl">
    <p v-if="!dp" class="hint">{{ t('db.needDateProp') }}</p>
    <div class="scroll">
      <div class="canvas" :style="{ width: days * COL + 'px' }">
        <div class="ticks"><div v-for="(d, i) in ticks" :key="i" class="tick" :class="{ mon: d.getDay() === 1 }" :style="{ width: COL + 'px' }">{{ d.getDate() === 1 || i === 0 ? (d.getMonth() + 1) + '/' : '' }}{{ d.getDate() }}</div></div>
        <div class="now" :style="{ left: todayX + COL / 2 + 'px' }" />
        <div v-for="x in items" :key="x.r.id" class="bar-row">
          <div class="bar" :style="{ left: pos(x.rg[0]) + 'px', width: Math.max(COL, pos(x.rg[1]) - pos(x.rg[0]) + COL) + 'px' }" @click="router.push(`/p/${x.r.id}`)">{{ x.r.title || t('common.untitled') }}</div>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.scroll { overflow-x: auto; border: 1px solid var(--border); border-radius: var(--radius-m); }
.canvas { position: relative; min-height: 120px; padding-bottom: 8px; }
.ticks { display: flex; border-bottom: 1px solid var(--border); position: sticky; top: 0; background: var(--bg); }
.tick { flex: none; font-size: 11px; color: var(--text-3); text-align: center; padding: 4px 0; border-right: 1px solid transparent; }
.tick.mon { border-right-color: var(--border); }
.now { position: absolute; top: 0; bottom: 0; width: 1px; background: var(--danger); opacity: .6; }
.bar-row { position: relative; height: 32px; }
.bar { position: absolute; top: 4px; height: 24px; line-height: 24px; padding: 0 8px; border-radius: 5px; background: var(--accent-soft); border: 1px solid var(--accent); color: var(--text); font-size: 12.5px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer; }
.hint { color: var(--text-3); font-size: 13px; }
</style>
