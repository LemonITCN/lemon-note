<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Btn from '@/components/ui/Btn.vue'
import type { DB } from './useDatabase'
import type { Filter } from '@/lib/db'

const props = defineProps<{ db: DB }>()
const { t } = useI18n()
const cfg = computed(() => props.db.view.value?.config || {})
const mode = computed(() => (cfg.value.filter?.or ? 'or' : 'and'))
const conds = computed<Filter[]>(() => cfg.value.filter?.and || cfg.value.filter?.or || [])

function opsFor(pid: string) {
  const ty = props.db.prop(pid)?.type || 'text'
  if (['number', 'formula', 'rollup'].includes(ty)) return ['=', '!=', '>', '<', '>=', '<=', 'is_empty', 'is_not_empty']
  if (['date', 'created_time', 'last_edited_time'].includes(ty)) return ['is', 'before', 'after', 'on_or_before', 'on_or_after', 'is_empty', 'is_not_empty']
  if (['select', 'status'].includes(ty)) return ['is', 'is_not', 'is_empty', 'is_not_empty']
  if (['multi_select', 'person', 'relation', 'files'].includes(ty)) return ['contains', 'not_contains', 'is_empty', 'is_not_empty']
  if (ty === 'checkbox') return ['is']
  return ['contains', 'not_contains', 'is', 'is_not', 'starts_with', 'ends_with', 'is_empty', 'is_not_empty']
}

function save(list: Filter[], m = mode.value) {
  props.db.updateView({ filter: list.length ? (m === 'or' ? { or: list } : { and: list }) : undefined })
}
function add() {
  const p = props.db.props.value[0]
  save([...conds.value, { prop: p.id, op: opsFor(p.id)[0], value: '' }])
}
function patch(i: number, ch: Partial<Filter>) {
  const list = conds.value.map((c) => ({ ...c }))
  Object.assign(list[i], ch)
  if (ch.prop) { list[i].op = opsFor(ch.prop)[0]; list[i].value = '' }
  save(list)
}
function remove(i: number) { const l = conds.value.slice(); l.splice(i, 1); save(l) }
function needsValue(op?: string) { return op !== 'is_empty' && op !== 'is_not_empty' }
</script>

<template>
  <div class="fe">
    <div v-for="(c, i) in conds" :key="i" class="cond">
      <span class="w">{{ i === 0 ? t('db.where') : '' }}
        <select v-if="i === 1" class="sel" :value="mode" @change="save(conds, ($event.target as HTMLSelectElement).value)"><option value="and">{{ t('db.and') }}</option><option value="or">{{ t('db.or') }}</option></select>
        <template v-else-if="i > 1">{{ mode === 'or' ? t('db.or') : t('db.and') }}</template>
      </span>
      <select class="sel" :value="c.prop" @change="patch(i, { prop: ($event.target as HTMLSelectElement).value })"><option v-for="p in db.props.value" :key="p.id" :value="p.id">{{ p.name }}</option></select>
      <select class="sel" :value="c.op" @change="patch(i, { op: ($event.target as HTMLSelectElement).value })"><option v-for="o in opsFor(c.prop || '')" :key="o" :value="o">{{ t('db.ops.' + o) }}</option></select>
      <template v-if="needsValue(c.op)">
        <select v-if="['select', 'status', 'multi_select'].includes(db.prop(c.prop || '')?.type || '')" class="sel grow" :value="c.value" @change="patch(i, { value: ($event.target as HTMLSelectElement).value })">
          <option value="" />
          <option v-for="o in db.prop(c.prop || '')?.options || []" :key="o.id" :value="o.name">{{ o.name }}</option>
        </select>
        <select v-else-if="db.prop(c.prop || '')?.type === 'checkbox'" class="sel grow" :value="String(c.value)" @change="patch(i, { value: ($event.target as HTMLSelectElement).value === 'true' })"><option value="true">{{ t('common.yes') }}</option><option value="false">{{ t('common.no') }}</option></select>
        <input v-else class="in grow" :type="db.prop(c.prop || '')?.type === 'date' ? 'date' : 'text'" :value="c.value" @change="patch(i, { value: ($event.target as HTMLInputElement).value })" />
      </template>
      <button class="x" @click="remove(i)"><Icon name="close" :size="13" /></button>
    </div>
    <Btn size="sm" variant="ghost" icon="plus" @click="add">{{ t('db.addFilter') }}</Btn>
  </div>
</template>

<style scoped>
.fe { display: flex; flex-direction: column; gap: 6px; min-width: 460px; max-width: 92vw; }
.cond { display: flex; align-items: center; gap: 6px; }
.w { width: 64px; font-size: 12.5px; color: var(--text-3); flex: none; }
.sel, .in { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 6px; background: var(--bg); font-size: 12.5px; color: var(--text); outline: none; max-width: 150px; }
.grow { flex: 1; max-width: none; }
.x { border: 0; background: none; color: var(--text-3); padding: 4px; display: flex; border-radius: 4px; }
.x:hover { background: var(--bg-hover); }
</style>
