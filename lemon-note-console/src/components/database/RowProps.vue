<script setup lang="ts">
// 数据库行页面顶部的属性面板。
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import CellValue from './CellValue.vue'
import CellEditor from './CellEditor.vue'
import FloatPanel from './FloatPanel.vue'
import PropertyEditor from './PropertyEditor.vue'
import { useDatabase } from './useDatabase'
import { TYPE_ICONS, COMPUTED, type DbProperty, type DbRow } from '@/lib/db'

const props = defineProps<{ databaseId: string; rowId: string; editable: boolean }>()
const { t } = useI18n()
const db = useDatabase(props.databaseId)
const editing = ref<{ prop: DbProperty; rect: DOMRect } | null>(null)
const propOpen = ref(false)
const propId = ref<string>()
const row = computed<DbRow | undefined>(() => db.result.value.rows.find((r) => r.id === props.rowId))
const list = computed(() => db.props.value.filter((p) => p.type !== 'title'))

onMounted(async () => {
  await db.load()
  db.temp.filter = undefined
  await db.query()
})

function edit(e: MouseEvent, p: DbProperty) {
  if (!props.editable || !row.value || COMPUTED.has(p.type)) return
  if (p.type === 'checkbox') { db.setProp(row.value, p.id, !(row.value.props?.[p.id] === true)); return }
  editing.value = { prop: p, rect: (e.currentTarget as HTMLElement).getBoundingClientRect() }
}
</script>

<template>
  <div v-if="row" class="rp">
    <div v-for="p in list" :key="p.id" class="pr">
      <button class="pn" @click="editable && ((propId = p.id), (propOpen = true))"><Icon :name="TYPE_ICONS[p.type]" :size="14" /><span class="ln-ellipsis">{{ p.name }}</span></button>
      <div class="pvv" :class="{ empty: !row.props?.[p.id] && !row.computed?.[p.id] }" @click="edit($event, p)">
        <CellValue :prop="p" :row="row" /><span v-if="!row.props?.[p.id] && !COMPUTED.has(p.type) && p.type !== 'checkbox'" class="ph">{{ t('db.empty') }}</span>
      </div>
    </div>
    <button v-if="editable" class="addp" @click="propId = undefined; propOpen = true"><Icon name="plus" :size="14" />{{ t('db.newProperty') }}</button>
    <FloatPanel v-if="editing" :anchor="editing.rect" @close="editing = null"><CellEditor :prop="editing.prop" :row="row" :db="db" @done="editing = null" /></FloatPanel>
    <PropertyEditor v-model:open="propOpen" :db="db" :prop-id="propId" />
  </div>
</template>

<style scoped>
.rp { display: flex; flex-direction: column; gap: 2px; padding: 4px 0 14px; margin-bottom: 10px; border-bottom: 1px solid var(--border); }
.pr { display: flex; align-items: flex-start; gap: 6px; min-height: 32px; }
.pn { width: 160px; flex: none; display: flex; align-items: center; gap: 6px; border: 0; background: none; color: var(--text-2); font-size: 13.5px; padding: 6px 6px; border-radius: 5px; text-align: left; min-width: 0; }
.pn:hover { background: var(--bg-hover); }
.pvv { flex: 1; min-width: 0; padding: 6px 8px; border-radius: 5px; font-size: 13.5px; cursor: pointer; display: flex; align-items: center; min-height: 32px; }
.pvv:hover { background: var(--bg-hover); }
.ph { color: var(--text-3); }
.addp { display: flex; align-items: center; gap: 6px; border: 0; background: none; color: var(--text-3); font-size: 13px; padding: 6px; border-radius: 5px; align-self: flex-start; }
.addp:hover { background: var(--bg-hover); }
@media (max-width: 760px) { .pn { width: 110px; } }
</style>
