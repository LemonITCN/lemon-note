<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import CellValue from './CellValue.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import type { DB } from './useDatabase'
import type { DbRow } from '@/lib/db'

const props = defineProps<{ db: DB; rows: DbRow[]; list?: boolean }>()
const { t } = useI18n()
const router = useRouter()
const shown = computed(() => props.db.visibleProps.value.filter((p) => p.type !== 'title'))
const coverProp = computed(() => props.db.props.value.find((p) => p.id === props.db.view.value?.config.card_cover) || props.db.props.value.find((p) => p.type === 'files'))
function has(r: DbRow, p: any) {
  const v = r.computed && p.id in r.computed ? r.computed[p.id] : r.props?.[p.id]
  return !(v === null || v === undefined || v === '' || v === false || (Array.isArray(v) && !v.length))
}
function cover(r: DbRow) {
  const f = coverProp.value ? r.props?.[coverProp.value.id]?.[0] : null
  if (!f) return ''
  return f.file_id ? '/api/files/' + f.file_id : f.url || ''
}
</script>

<template>
  <div :class="list ? 'lv' : 'gv'">
    <div v-for="r in rows" :key="r.id" class="item" @click="router.push(`/p/${r.id}`)">
      <div v-if="!list" class="cover"><img v-if="cover(r)" :src="cover(r)" alt="" loading="lazy" /><PageIcon v-else :icon="r.icon" :size="28" /></div>
      <div class="body">
        <div class="tt"><PageIcon v-if="list || cover(r)" :icon="r.icon" :size="15" /><span class="ln-ellipsis">{{ r.title || t('common.untitled') }}</span></div>
        <div class="props"><template v-for="p in shown" :key="p.id"><span v-if="has(r, p)" class="pv"><CellValue :prop="p" :row="r" /></span></template></div>
      </div>
    </div>
    <button v-if="db.canEdit.value" class="add" @click="db.addRow({})"><Icon name="plus" :size="15" />{{ t('db.newRow') }}</button>
  </div>
</template>

<style scoped>
.gv { display: grid; grid-template-columns: repeat(auto-fill, minmax(220px, 1fr)); gap: 12px; padding: 6px 0 12px; }
.lv { display: flex; flex-direction: column; padding: 4px 0 12px; }
.item { cursor: pointer; }
.gv .item { border: 1px solid var(--border); border-radius: var(--radius-l); overflow: hidden; background: var(--bg-elev); box-shadow: var(--shadow-soft); }
.gv .item:hover { background: var(--bg-soft); }
.lv .item { display: flex; align-items: center; border-bottom: 1px solid var(--border); padding: 6px 4px; }
.lv .item:hover { background: var(--bg-hover); }
.cover { height: 120px; background: var(--bg-sunken); display: flex; align-items: center; justify-content: center; }
.cover img { width: 100%; height: 100%; object-fit: cover; }
.body { padding: 8px 10px; display: flex; flex-direction: column; gap: 6px; min-width: 0; flex: 1; }
.lv .body { flex-direction: row; align-items: center; padding: 0 4px; }
.tt { display: flex; align-items: center; gap: 6px; font-weight: 500; font-size: 13.5px; min-width: 0; }
.lv .tt { flex: 1; }
.props { display: flex; flex-wrap: wrap; gap: 6px; font-size: 12.5px; }
.pv:empty { display: none; }
.add { display: flex; align-items: center; justify-content: center; gap: 6px; border: 1px dashed var(--border-strong); background: none; color: var(--text-3); border-radius: var(--radius-l); min-height: 60px; font-size: 13px; }
.lv .add { border: 0; justify-content: flex-start; min-height: 0; padding: 8px 4px; }
.add:hover { background: var(--bg-hover); }
</style>
