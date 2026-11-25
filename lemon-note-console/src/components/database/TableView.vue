<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import CellValue from './CellValue.vue'
import CellEditor from './CellEditor.vue'
import FloatPanel from './FloatPanel.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import type { DB } from './useDatabase'
import type { DbRow, DbProperty } from '@/lib/db'
import { TYPE_ICONS } from '@/lib/db'

const props = defineProps<{ db: DB; rows: DbRow[] }>()
const emit = defineEmits<{ editProp: [id?: string] }>()
const { t } = useI18n()
const router = useRouter()
const editing = ref<{ row: DbRow; prop: DbProperty; rect: DOMRect } | null>(null)
const cols = computed(() => props.db.visibleProps.value)
const widths = computed(() => props.db.view.value?.config.widths || {})
const w = (p: DbProperty) => widths.value[p.id] || (p.type === 'title' ? 260 : p.type === 'checkbox' ? 90 : 180)

function edit(e: MouseEvent, row: DbRow, p: DbProperty) {
  if (!props.db.canEdit.value) return
  if (p.type === 'checkbox') { props.db.setProp(row, p.id, !(row.props?.[p.id] === true)); return }
  if (['formula', 'rollup', 'created_time', 'created_by', 'last_edited_time', 'last_edited_by', 'unique_id'].includes(p.type)) return
  editing.value = { row, prop: p, rect: (e.currentTarget as HTMLElement).getBoundingClientRect() }
}

function headerMenu(p: DbProperty) {
  return [
    { label: t('db.editProperty'), icon: 'settings', disabled: !props.db.canEdit.value, run: () => emit('editProp', p.id) },
    { label: t('db.asc'), icon: 'arrow-up', run: () => props.db.updateView({ sorts: [{ prop: p.id, dir: 'asc' }] }) },
    { label: t('db.desc'), icon: 'arrow-down', run: () => props.db.updateView({ sorts: [{ prop: p.id, dir: 'desc' }] }) },
    ...(p.type !== 'title' ? [{ label: t('db.hide'), icon: 'eye-off', run: () => props.db.updateView({ visible: cols.value.filter((c) => c.id !== p.id).map((c) => c.id) }) }] : []),
  ]
}

function startResize(e: PointerEvent, p: DbProperty) {
  e.preventDefault()
  const x0 = e.clientX
  const w0 = w(p)
  let cur = w0
  const move = (ev: PointerEvent) => {
    cur = Math.max(60, Math.min(800, w0 + ev.clientX - x0))
    props.db.view.value!.config.widths = { ...widths.value, [p.id]: cur }
  }
  const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up); props.db.updateView({ widths: { ...widths.value, [p.id]: cur } }) }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

async function addRow() { await props.db.addRow({}) }
</script>

<template>
  <div class="tv">
    <div class="tbl">
      <div class="hr">
        <div v-for="p in cols" :key="p.id" class="hc" :style="{ width: w(p) + 'px' }">
          <Menu :items="headerMenu(p)"><button class="hbtn"><Icon :name="TYPE_ICONS[p.type] || 'text'" :size="14" /><span class="ln-ellipsis">{{ p.name }}</span></button></Menu>
          <span class="rz" @pointerdown="startResize($event, p)" />
        </div>
        <button v-if="db.canEdit.value" class="hc add" @click="emit('editProp')"><Icon name="plus" :size="15" /></button>
      </div>
      <div v-for="r in rows" :key="r.id" class="tr">
        <div v-for="p in cols" :key="p.id" class="td" :class="{ title: p.type === 'title' }" :style="{ width: w(p) + 'px' }" @click="edit($event, r, p)">
          <template v-if="p.type === 'title'">
            <PageIcon v-if="r.icon" :icon="r.icon" :size="15" />
            <span class="ln-ellipsis tt">{{ r.title || t('common.untitled') }}</span>
            <button class="open" @click.stop="router.push(`/p/${r.id}`)">{{ t('common.open') }}</button>
          </template>
          <CellValue v-else :prop="p" :row="r" />
        </div>
      </div>
      <button v-if="db.canEdit.value" class="newrow" @click="addRow"><Icon name="plus" :size="14" />{{ t('db.newRow') }}</button>
      <div v-if="db.result.value.calcs" class="calc">
        <div v-for="p in cols" :key="p.id" class="cc" :style="{ width: w(p) + 'px' }">{{ db.result.value.calcs[p.id] ?? '' }}</div>
      </div>
    </div>
    <FloatPanel v-if="editing" :anchor="editing.rect" @close="editing = null">
      <CellEditor :prop="editing.prop" :row="editing.row" :db="db" @done="editing = null" />
    </FloatPanel>
  </div>
</template>

<style scoped>
.tv { overflow-x: auto; padding-bottom: 6px; }
.tbl { display: inline-block; min-width: 100%; }
.hr, .tr, .calc { display: flex; border-bottom: 1px solid var(--border); }
.hr { border-top: 1px solid var(--border); }
.hc, .td, .cc { flex: none; border-right: 1px solid var(--border); min-height: 34px; position: relative; }
.hc { display: flex; align-items: center; }
.hc.add { width: 40px; border: 0; background: none; color: var(--text-3); justify-content: center; }
.hc.add:hover { background: var(--bg-hover); }
.hbtn { display: flex; align-items: center; gap: 6px; width: 100%; height: 34px; padding: 0 8px; border: 0; background: none; color: var(--text-2); font-size: 13px; text-align: left; min-width: 0; }
.hbtn:hover { background: var(--bg-hover); }
.rz { position: absolute; right: -3px; top: 0; bottom: 0; width: 6px; cursor: col-resize; z-index: 2; }
.rz:hover { background: var(--accent); }
.td { padding: 6px 8px; font-size: 13.5px; display: flex; align-items: center; gap: 6px; cursor: default; overflow: hidden; }
.td:hover { background: var(--bg-hover); }
.td.title { font-weight: 500; }
.tt { flex: 1; min-width: 0; }
.open { display: none; border: 1px solid var(--border-strong); background: var(--bg-elev); color: var(--text-2); font-size: 11.5px; padding: 1px 6px; border-radius: 4px; flex: none; }
.td.title:hover .open { display: inline-block; }
.newrow { display: flex; align-items: center; gap: 6px; border: 0; background: none; color: var(--text-3); font-size: 13px; padding: 7px 8px; width: 100%; text-align: left; border-bottom: 1px solid var(--border); }
.newrow:hover { background: var(--bg-hover); color: var(--text-2); }
.calc { border-bottom: 0; }
.cc { border-right: 0; padding: 6px 8px; font-size: 12px; color: var(--text-3); text-align: right; }
</style>
