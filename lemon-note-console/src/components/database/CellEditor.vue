<script setup lang="ts">
// 单元格编辑器（放在弹出层里）。
import { ref, computed, onMounted, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import type { DbProperty, DbRow, DbOption } from '@/lib/db'
import { newOptionId, dbApi } from '@/lib/db'
import type { DB } from './useDatabase'
import Icon from '@/components/icons/Icon.vue'
import { useUsers } from '@/lib/users'
import { COLORS } from '@/components/editor/extensions/marks'
import { uploadFile, pickFile } from '@/lib/files'

const props = defineProps<{ prop: DbProperty; row: DbRow; db: DB }>()
const emit = defineEmits<{ done: [] }>()
const { t } = useI18n()
const users = useUsers()
const raw = computed(() => props.row.props?.[props.prop.id])
const text = ref(props.prop.type === 'title' ? props.row.title : raw.value ?? '')
const q = ref('')
const input = ref<HTMLInputElement>()
const dateStart = ref(raw.value?.start || '')
const dateEnd = ref(raw.value?.end || '')
const withEnd = ref(!!raw.value?.end)
const relRows = ref<{ id: string; title: string }[]>([])

onMounted(async () => {
  await nextTick()
  input.value?.focus()
  if (props.prop.type === 'relation' && props.prop.database_id) searchRel()
})

function commitText() {
  const v = typeof text.value === 'string' ? text.value.trim() : text.value
  if (props.prop.type === 'number') props.db.setProp(props.row, props.prop.id, v === '' ? null : Number(v))
  else props.db.setProp(props.row, props.prop.id, v === '' ? null : v)
  emit('done')
}

const options = computed(() => (props.prop.options || []).filter((o) => !q.value || o.name.toLowerCase().includes(q.value.toLowerCase())))
const selected = computed<string[]>(() => (Array.isArray(raw.value) ? raw.value : raw.value ? [raw.value] : []))
const multi = computed(() => props.prop.type === 'multi_select')

async function pickOption(o: DbOption) {
  if (multi.value) {
    const cur = selected.value
    const next = cur.includes(o.id) ? cur.filter((x) => x !== o.id) : [...cur, o.id]
    await props.db.setProp(props.row, props.prop.id, next)
  } else {
    await props.db.setProp(props.row, props.prop.id, selected.value[0] === o.id ? null : o.id)
    emit('done')
  }
}

async function createOption() {
  const name = q.value.trim()
  if (!name) return
  const next = JSON.parse(JSON.stringify(props.db.schema.value))
  const p = next.properties.find((x: DbProperty) => x.id === props.prop.id)
  const o = { id: newOptionId(), name, color: COLORS[(p.options?.length || 0) % COLORS.length] }
  p.options = [...(p.options || []), o]
  if (await props.db.saveSchema(next)) {
    q.value = ''
    await pickOption(o)
  }
}

function saveDate() {
  if (!dateStart.value) props.db.setProp(props.row, props.prop.id, null)
  else props.db.setProp(props.row, props.prop.id, withEnd.value && dateEnd.value ? { start: dateStart.value, end: dateEnd.value } : { start: dateStart.value })
}

async function togglePerson(id: string) {
  const cur = selected.value
  await props.db.setProp(props.row, props.prop.id, cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id])
}

let st: number | undefined
function searchRel() {
  clearTimeout(st)
  st = window.setTimeout(async () => {
    try {
      const r = await dbApi.query(props.prop.database_id!, { search: q.value, limit: 30 })
      relRows.value = r.rows.map((x) => ({ id: x.id, title: x.title }))
    } catch { relRows.value = [] }
  }, 150)
}

async function toggleRel(id: string) {
  const cur = selected.value
  await props.db.setProp(props.row, props.prop.id, cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id])
}

async function addFile() {
  const f = await pickFile()
  if (!f) return
  const r = await uploadFile(f, props.row.id)
  await props.db.setProp(props.row, props.prop.id, [...(raw.value || []), { file_id: r.id, name: r.name }])
}
function removeFile(i: number) {
  const list = [...(raw.value || [])]
  list.splice(i, 1)
  props.db.setProp(props.row, props.prop.id, list)
}
</script>

<template>
  <div class="ce">
    <template v-if="['title', 'text', 'url', 'email', 'phone', 'number'].includes(prop.type)">
      <textarea v-if="prop.type === 'text' || prop.type === 'title'" ref="input" v-model="text" class="ta" rows="3" @keydown.enter.exact.prevent="commitText" @keydown.esc="emit('done')" />
      <input v-else ref="input" v-model="text" class="in" :type="prop.type === 'number' ? 'number' : 'text'" @keydown.enter.prevent="commitText" @keydown.esc="emit('done')" @blur="commitText" />
    </template>

    <template v-else-if="['select', 'multi_select', 'status'].includes(prop.type)">
      <input ref="input" v-model="q" class="in" :placeholder="t('db.searchOrCreate')" @keydown.enter.prevent="options[0] ? pickOption(options[0]) : createOption()" />
      <div class="opts">
        <button v-for="o in options" :key="o.id" class="opt" @click="pickOption(o)">
          <span class="tag" :class="'cb-' + (o.color || 'gray')">{{ o.name }}</span>
          <span class="ln-spacer" /><Icon v-if="selected.includes(o.id)" name="check" :size="14" />
        </button>
        <button v-if="q.trim() && !options.some((o) => o.name === q.trim()) && prop.type !== 'status'" class="opt create" @click="createOption"><Icon name="plus" :size="14" />{{ t('db.createOption', { name: q.trim() }) }}</button>
      </div>
    </template>

    <template v-else-if="prop.type === 'date'">
      <input ref="input" v-model="dateStart" class="in" type="date" @change="saveDate" />
      <label class="row"><input v-model="withEnd" type="checkbox" @change="saveDate" />{{ t('db.endDate') }}</label>
      <input v-if="withEnd" v-model="dateEnd" class="in" type="date" @change="saveDate" />
      <button class="clear" @click="dateStart = ''; saveDate(); emit('done')">{{ t('common.remove') }}</button>
    </template>

    <template v-else-if="prop.type === 'person'">
      <div class="opts">
        <button v-for="u in users.list" :key="u.id" class="opt" @click="togglePerson(u.id)">
          <span>{{ u.display_name }}</span><span class="sub">@{{ u.username }}</span><span class="ln-spacer" /><Icon v-if="selected.includes(u.id)" name="check" :size="14" />
        </button>
      </div>
    </template>

    <template v-else-if="prop.type === 'relation'">
      <input ref="input" v-model="q" class="in" :placeholder="t('common.search')" @input="searchRel" />
      <div class="opts">
        <button v-for="r in relRows" :key="r.id" class="opt" @click="toggleRel(r.id)">
          <Icon name="page" :size="14" /><span class="ln-ellipsis">{{ r.title || t('common.untitled') }}</span><span class="ln-spacer" /><Icon v-if="selected.includes(r.id)" name="check" :size="14" />
        </button>
      </div>
    </template>

    <template v-else-if="prop.type === 'files'">
      <div class="opts">
        <div v-for="(f, i) in ((raw || []) as any[])" :key="i" class="opt"><Icon name="paperclip" :size="14" /><a :href="f.file_id ? '/api/files/' + f.file_id : f.url" target="_blank" class="ln-ellipsis">{{ f.name }}</a><span class="ln-spacer" /><button class="x" @click="removeFile(i)"><Icon name="close" :size="12" /></button></div>
      </div>
      <button class="clear" @click="addFile"><Icon name="upload" :size="14" />{{ t('editor.file') }}</button>
    </template>

    <p v-else class="ro">{{ t('db.computed') }}</p>
  </div>
</template>

<style scoped>
.ce { width: 280px; display: flex; flex-direction: column; gap: 6px; }
.in, .ta { width: 100%; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 6px 8px; background: var(--bg); outline: none; font-size: 13.5px; resize: vertical; }
.in:focus, .ta:focus { border-color: var(--accent); }
.opts { max-height: 260px; overflow: auto; display: flex; flex-direction: column; }
.opt { display: flex; align-items: center; gap: 6px; border: 0; background: none; padding: 5px 6px; border-radius: 5px; text-align: left; font-size: 13px; color: var(--text); min-width: 0; }
.opt:hover { background: var(--bg-hover); }
.opt a { color: inherit; }
.opt.create { color: var(--text-2); }
.sub { color: var(--text-3); font-size: 12px; }
.tag { padding: 0 7px; height: 20px; display: inline-flex; align-items: center; border-radius: 4px; font-size: 12.5px; }
.row { display: flex; align-items: center; gap: 6px; font-size: 13px; color: var(--text-2); }
.clear { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12.5px; padding: 4px 6px; border-radius: 4px; align-self: flex-start; }
.clear:hover { background: var(--bg-hover); }
.x { border: 0; background: none; color: var(--text-3); padding: 2px; display: flex; }
.ro { color: var(--text-3); font-size: 12.5px; margin: 4px; }
</style>
