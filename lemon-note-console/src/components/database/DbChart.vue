<script setup lang="ts">
import { ref, watch, onMounted, onBeforeUnmount, computed, shallowRef } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DB } from './useDatabase'
import { api } from '@/api/client'
import { useUI } from '@/stores/ui'

const props = defineProps<{ db: DB; dbId: string }>()
const { t } = useI18n()
const ui = useUI()
const el = ref<HTMLDivElement>()
const chart = shallowRef<any>(null)
const cfg = computed(() => props.db.view.value?.config.chart || {})
const xs = computed(() => props.db.props.value.filter((p) => ['select', 'status', 'multi_select', 'person', 'checkbox', 'date', 'text', 'title', 'created_time'].includes(p.type)))
const ys = computed(() => props.db.props.value.filter((p) => ['number', 'formula', 'rollup'].includes(p.type)))

function set(k: string, v: any) { props.db.updateView({ chart: { ...cfg.value, [k]: v } }) }

async function render() {
  if (!el.value) return
  const echarts = await import('echarts')
  chart.value?.dispose()
  chart.value = echarts.init(el.value, ui.effectiveTheme === 'dark' ? 'dark' : undefined, { renderer: 'svg' })
  const y = cfg.value.y ? [{ property: cfg.value.y, agg: cfg.value.agg || 'sum' }] : []
  try {
    const r = await api.post('/api/charts/query', { type: cfg.value.type || 'bar', source: { kind: 'database', databaseId: props.dbId, viewId: props.db.viewId.value, x: cfg.value.x || '', y } })
    chart.value.setOption({ backgroundColor: 'transparent', ...r.option })
  } catch {}
}
watch(() => [cfg.value, props.db.result.value, ui.effectiveTheme], render, { deep: true })
let ro: ResizeObserver | null = null
onMounted(() => { render(); ro = new ResizeObserver(() => chart.value?.resize()); if (el.value) ro.observe(el.value) })
onBeforeUnmount(() => { ro?.disconnect(); chart.value?.dispose() })
</script>

<template>
  <div class="dc">
    <div class="opts">
      <select class="sel" :value="cfg.type || 'bar'" @change="set('type', ($event.target as HTMLSelectElement).value)">
        <option v-for="ty in ['bar', 'line', 'area', 'pie']" :key="ty" :value="ty">{{ t('db.chartTypes.' + ty) }}</option>
      </select>
      <span>{{ t('db.xAxis') }}</span>
      <select class="sel" :value="cfg.x || ''" @change="set('x', ($event.target as HTMLSelectElement).value)"><option v-for="p in xs" :key="p.id" :value="p.id">{{ p.name }}</option></select>
      <span>{{ t('db.yAxis') }}</span>
      <select class="sel" :value="cfg.y || ''" @change="set('y', ($event.target as HTMLSelectElement).value)"><option value="">{{ t('db.count') }}</option><option v-for="p in ys" :key="p.id" :value="p.id">{{ p.name }}</option></select>
      <select v-if="cfg.y" class="sel" :value="cfg.agg || 'sum'" @change="set('agg', ($event.target as HTMLSelectElement).value)"><option v-for="a in ['sum', 'average', 'min', 'max', 'median']" :key="a" :value="a">{{ t('db.fn.' + a) }}</option></select>
    </div>
    <div ref="el" class="chart" />
  </div>
</template>

<style scoped>
.opts { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-2); padding: 4px 0 8px; flex-wrap: wrap; }
.sel { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 6px; background: var(--bg); font-size: 12.5px; color: var(--text); }
.chart { height: 360px; }
</style>
