<script setup lang="ts">
// 属性设置：名称、类型、选项、数字格式、公式、关联、汇总。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Icon from '@/components/icons/Icon.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import type { DbProperty, DbSchema } from '@/lib/db'
import { PROP_TYPES, TYPE_ICONS, newPropId, newOptionId, dbApi } from '@/lib/db'
import { COLORS } from '@/components/editor/extensions/marks'
import { useWorkspace } from '@/stores/workspace'
import type { DB } from './useDatabase'
import { confirmDialog } from '@/lib/confirm'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ db: DB; propId?: string }>()
const { t } = useI18n()
const ws = useWorkspace()
const draft = ref<DbProperty>({ id: '', name: '', type: 'text' })
const busy = ref(false)
const isNew = computed(() => !props.propId)
const otherDbs = computed(() => [...ws.pages.values()].filter((p) => p.kind === 'database' && !p.trashed_at))
const relationProps = computed(() => props.db.props.value.filter((p) => p.type === 'relation'))
const targetProps = ref<DbProperty[]>([])

watch(open, (v) => {
  if (!v) return
  const cur = props.db.props.value.find((p) => p.id === props.propId)
  draft.value = cur ? JSON.parse(JSON.stringify(cur)) : { id: newPropId(), name: '', type: 'text' }
  if (draft.value.type === 'rollup') loadTargets()
})

async function loadTargets() {
  const rel = props.db.props.value.find((p) => p.id === draft.value.relation)
  if (!rel?.database_id) { targetProps.value = []; return }
  try { targetProps.value = (await dbApi.info(rel.database_id)).schema.properties } catch { targetProps.value = [] }
}

function addOption() {
  const opts = draft.value.options || (draft.value.options = [])
  opts.push({ id: newOptionId(), name: t('db.option') + ' ' + (opts.length + 1), color: COLORS[opts.length % COLORS.length], group: draft.value.type === 'status' ? 'todo' : undefined })
}

async function save() {
  busy.value = true
  const next: DbSchema = JSON.parse(JSON.stringify(props.db.schema.value))
  const d = { ...draft.value, name: draft.value.name.trim() || t('db.property') }
  if (d.type === 'status' && !d.options?.length) d.options = [{ id: newOptionId(), name: t('db.todo'), color: 'gray', group: 'todo' }, { id: newOptionId(), name: t('db.doing'), color: 'blue', group: 'doing' }, { id: newOptionId(), name: t('db.done'), color: 'green', group: 'done' }]
  const i = next.properties.findIndex((p) => p.id === d.id)
  if (i >= 0) next.properties[i] = d
  else next.properties.push(d)
  if (await props.db.saveSchema(next)) open.value = false
  busy.value = false
}

async function remove() {
  if (!(await confirmDialog({ title: t('db.deleteProperty'), message: draft.value.name, danger: true, okText: t('common.delete') }))) return
  const next: DbSchema = JSON.parse(JSON.stringify(props.db.schema.value))
  next.properties = next.properties.filter((p) => p.id !== draft.value.id)
  if (await props.db.saveSchema(next)) open.value = false
}
</script>

<template>
  <Modal v-model:open="open" :title="isNew ? t('db.newProperty') : t('db.editProperty')" :width="460">
    <div class="pe">
      <TextField v-model="draft.name" :label="t('common.name')" autofocus />
      <div v-if="draft.type !== 'title'" class="field">
        <span class="lbl">{{ t('db.type') }}</span>
        <div class="types">
          <button v-for="ty in PROP_TYPES" :key="ty" class="ty" :class="{ on: draft.type === ty }" @click="draft.type = ty; ty === 'rollup' && loadTargets()">
            <Icon :name="TYPE_ICONS[ty]" :size="15" />{{ t('db.types.' + ty) }}
          </button>
        </div>
      </div>
      <div v-if="['select', 'multi_select', 'status'].includes(draft.type)" class="field">
        <span class="lbl">{{ t('db.options') }}</span>
        <div v-for="(o, i) in draft.options || []" :key="o.id" class="optrow">
          <select v-model="o.color" class="sel" :class="'cb-' + o.color"><option v-for="c in COLORS" :key="c" :value="c">{{ t('colors.' + c) }}</option></select>
          <input v-model="o.name" class="in" />
          <select v-if="draft.type === 'status'" v-model="o.group" class="sel"><option value="todo">{{ t('db.todo') }}</option><option value="doing">{{ t('db.doing') }}</option><option value="done">{{ t('db.done') }}</option></select>
          <button class="x" @click="draft.options!.splice(i, 1)"><Icon name="close" :size="13" /></button>
        </div>
        <Btn size="sm" variant="ghost" icon="plus" @click="addOption">{{ t('db.addOption') }}</Btn>
      </div>
      <div v-if="draft.type === 'number'" class="field">
        <span class="lbl">{{ t('db.numberFormat') }}</span>
        <select v-model="draft.format" class="sel wide"><option value="">{{ t('db.fmt.number') }}</option><option value="comma">{{ t('db.fmt.comma') }}</option><option value="percent">{{ t('db.fmt.percent') }}</option><option value="yuan">{{ t('db.fmt.yuan') }}</option><option value="dollar">{{ t('db.fmt.dollar') }}</option></select>
      </div>
      <div v-if="draft.type === 'formula'" class="field">
        <span class="lbl">{{ t('db.types.formula') }}<InfoTip :text="t('db.formulaTip')" /></span>
        <textarea v-model="draft.expression" class="in mono" rows="3" placeholder='prop("数量") * prop("单价")' />
      </div>
      <div v-if="draft.type === 'relation'" class="field">
        <span class="lbl">{{ t('db.relatedDb') }}</span>
        <select v-model="draft.database_id" class="sel wide" :disabled="!isNew && !!draft.related_prop"><option v-for="d in otherDbs" :key="d.id" :value="d.id">{{ d.title || t('common.untitled') }}</option></select>
        <label class="chk"><input v-model="draft.two_way" type="checkbox" :disabled="!isNew" />{{ t('db.twoWay') }}<InfoTip :text="t('db.twoWayTip')" /></label>
      </div>
      <div v-if="draft.type === 'rollup'" class="field">
        <span class="lbl">{{ t('db.viaRelation') }}</span>
        <select v-model="draft.relation" class="sel wide" @change="loadTargets"><option v-for="p in relationProps" :key="p.id" :value="p.id">{{ p.name }}</option></select>
        <span class="lbl">{{ t('db.targetProp') }}</span>
        <select v-model="draft.target" class="sel wide"><option v-for="p in targetProps" :key="p.id" :value="p.id">{{ p.name }}</option></select>
        <span class="lbl">{{ t('db.calc') }}</span>
        <select v-model="draft.function" class="sel wide">
          <option v-for="f in ['show_original', 'count', 'count_values', 'unique', 'empty', 'not_empty', 'percent_empty', 'sum', 'average', 'median', 'min', 'max', 'range', 'earliest', 'latest', 'checked']" :key="f" :value="f">{{ t('db.fn.' + f) }}</option>
        </select>
      </div>
      <div v-if="draft.type === 'unique_id'" class="field"><TextField v-model="draft.prefix" :label="t('db.prefix')" /></div>
    </div>
    <template #footer>
      <Btn v-if="!isNew && draft.type !== 'title'" variant="ghost" icon="trash" @click="remove">{{ t('common.delete') }}</Btn>
      <span class="ln-spacer" />
      <Btn @click="open = false">{{ t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="busy" @click="save">{{ t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.pe { display: flex; flex-direction: column; gap: 14px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.lbl { font-size: 12.5px; font-weight: 500; color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.types { display: grid; grid-template-columns: repeat(3, 1fr); gap: 4px; }
.ty { display: flex; align-items: center; gap: 6px; border: 1px solid var(--border); background: var(--bg); padding: 6px 8px; border-radius: var(--radius-m); font-size: 12.5px; color: var(--text-2); text-align: left; }
.ty:hover { border-color: var(--border-strong); color: var(--text); }
.ty.on { border-color: var(--accent); color: var(--accent); background: var(--accent-soft); }
.optrow { display: flex; gap: 6px; align-items: center; }
.in { flex: 1; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 5px 8px; background: var(--bg); outline: none; font-size: 13px; resize: vertical; }
.in.mono { font-family: var(--font-mono); }
.sel { border: 1px solid var(--border-strong); border-radius: var(--radius-m); height: 30px; padding: 0 6px; background: var(--bg); font-size: 12.5px; color: var(--text); }
.sel.wide { width: 100%; }
.x { border: 0; background: none; color: var(--text-3); padding: 4px; display: flex; border-radius: 4px; }
.x:hover { background: var(--bg-hover); }
.chk { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); }
</style>
