<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import CellValue from './CellValue.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import type { DB } from './useDatabase'
import type { DbRow, DbGroup } from '@/lib/db'

const props = defineProps<{ db: DB; groups: DbGroup[] }>()
const { t } = useI18n()
const router = useRouter()
const dragging = ref<DbRow | null>(null)
const over = ref('')
const groupProp = computed(() => props.db.prop(props.db.view.value?.config.group_by?.prop || ''))
const cardProps = computed(() => props.db.visibleProps.value.filter((p) => p.type !== 'title' && p.id !== groupProp.value?.id))

function hasValue(r: DbRow, p: any) {
  const v = r.computed && p.id in r.computed ? r.computed[p.id] : r.props?.[p.id]
  if (v === null || v === undefined || v === '' || v === false) return false
  return !(Array.isArray(v) && !v.length)
}

function valueFor(key: string) {
  const p = groupProp.value
  if (!p) return null
  if (p.type === 'checkbox') return key === 'true'
  if (p.type === 'multi_select' || p.type === 'person' || p.type === 'relation') return key ? [key] : []
  if (p.type === 'select' || p.type === 'status') return key || null
  return key || null
}

async function drop(g: DbGroup, beforeIdx: number) {
  const row = dragging.value
  dragging.value = null
  over.value = ''
  if (!row || !groupProp.value) return
  const rows = g.rows.filter((r) => r.id !== row.id)
  const after = beforeIdx <= 0 ? null : rows[Math.min(beforeIdx, rows.length) - 1]?.id ?? null
  const extra: Record<string, any> = {}
  if (!g.rows.some((r) => r.id === row.id)) extra[groupProp.value.id] = valueFor(g.key)
  await props.db.moveRow(row, after, Object.keys(extra).length ? extra : undefined)
}

async function add(g: DbGroup) {
  const p = groupProp.value
  await props.db.addRow(p ? { [p.id]: valueFor(g.key) } : {})
}
</script>

<template>
  <div class="bv">
    <div v-for="g in groups" :key="g.key" class="col" :class="{ over: over === g.key }" @dragover.prevent="over = g.key" @dragleave="over = ''" @drop="drop(g, g.rows.length)">
      <div class="ch">
        <span class="tag" :class="'cb-' + (g.color || 'gray')">{{ g.label || t('db.noValue') }}</span>
        <span class="cnt">{{ g.count }}</span>
      </div>
      <div v-for="(r, i) in g.rows" :key="r.id" class="card" :draggable="db.canEdit.value" @dragstart="dragging = r" @dragend="dragging = null" @drop.stop="drop(g, i)" @click="router.push(`/p/${r.id}`)">
        <div class="ct"><PageIcon v-if="r.icon" :icon="r.icon" :size="15" /><span>{{ r.title || t('common.untitled') }}</span></div>
        <template v-for="p in cardProps" :key="p.id"><div v-if="hasValue(r, p)" class="cp"><CellValue :prop="p" :row="r" /></div></template>
      </div>
      <button v-if="db.canEdit.value" class="add" @click="add(g)"><Icon name="plus" :size="14" />{{ t('db.newRow') }}</button>
    </div>
    <p v-if="!groupProp" class="hint">{{ t('db.boardNeedsGroup') }}</p>
  </div>
</template>

<style scoped>
.bv { display: flex; gap: 12px; overflow-x: auto; padding: 6px 0 12px; align-items: flex-start; }
.col { width: 260px; flex: none; display: flex; flex-direction: column; gap: 6px; border-radius: var(--radius-l); padding: 4px; }
.col.over { background: var(--accent-soft); }
.ch { display: flex; align-items: center; gap: 6px; padding: 4px 4px 6px; }
.tag { padding: 0 7px; height: 20px; display: inline-flex; align-items: center; border-radius: 4px; font-size: 12.5px; }
.cnt { color: var(--text-3); font-size: 12px; }
.card { background: var(--bg-elev); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; display: flex; flex-direction: column; gap: 6px; cursor: pointer; box-shadow: var(--shadow-soft); }
.card:hover { background: var(--bg-soft); }
.ct { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 13.5px; }
.cp { font-size: 12.5px; }
.cp:empty { display: none; }
.add { display: flex; align-items: center; gap: 6px; border: 0; background: none; color: var(--text-3); font-size: 13px; padding: 6px; border-radius: 5px; text-align: left; }
.add:hover { background: var(--bg-hover); }
.hint { color: var(--text-3); font-size: 13px; }
</style>
