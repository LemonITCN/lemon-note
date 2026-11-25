<script setup lang="ts">
import { ref, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import Btn from '@/components/ui/Btn.vue'
import FilterEditor from './FilterEditor.vue'
import SortEditor from './SortEditor.vue'
import type { DB } from './useDatabase'
import { VIEW_ICONS, TYPE_ICONS } from '@/lib/db'
import { confirmDialog } from '@/lib/confirm'
import { isPublic } from '@/lib/publicCtx'

const props = defineProps<{ db: DB }>()
// 公开分享页只按视图查看：筛选、排序、分组等临时条件服务器不接受，就不显示
const publicMode = isPublic()
const emit = defineEmits<{ newRow: []; newProp: [] }>()
const { t } = useI18n()
const searching = ref(false)
const v = computed(() => props.db.view.value)
const filterCount = computed(() => (v.value?.config.filter?.and || v.value?.config.filter?.or || []).length)
const sortCount = computed(() => v.value?.config.sorts?.length || 0)

const addViewMenu = computed(() => Object.keys(VIEW_ICONS).map((ty) => ({ label: t('db.views.' + ty), icon: VIEW_ICONS[ty], run: () => props.db.addView(ty, t('db.views.' + ty)) })))

function viewMenu(id: string) {
  const view = props.db.views.value.find((x) => x.id === id)!
  return [
    { label: t('common.rename'), icon: 'text', run: () => { const n = window.prompt(t('common.rename'), view.name); if (n?.trim()) { props.db.viewId.value = id; props.db.updateView({}, { name: n.trim() }) } } },
    { heading: t('db.layout') },
    ...Object.keys(VIEW_ICONS).map((ty) => ({ label: t('db.views.' + ty), icon: VIEW_ICONS[ty], checked: view.type === ty, run: () => { props.db.viewId.value = id; props.db.updateView({}, { type: ty }) } })),
    { separator: true },
    { label: t('common.delete'), icon: 'trash', danger: true, disabled: props.db.views.value.length <= 1, run: async () => { if (await confirmDialog({ title: t('db.deleteView'), message: view.name, danger: true })) props.db.deleteView(id) } },
  ]
}

const groupable = computed(() => props.db.props.value.filter((p) => ['select', 'status', 'multi_select', 'person', 'checkbox', 'date', 'text', 'relation'].includes(p.type)))
const groupMenu = computed(() => [
  { label: t('common.none'), checked: !v.value?.config.group_by, run: () => props.db.updateView({ group_by: undefined }) },
  ...groupable.value.map((p) => ({ label: p.name, icon: TYPE_ICONS[p.type], checked: v.value?.config.group_by?.prop === p.id, run: () => props.db.updateView({ group_by: { prop: p.id } }) })),
])
const dateProps = computed(() => props.db.props.value.filter((p) => ['date', 'created_time', 'last_edited_time'].includes(p.type)))
const dateMenu = computed(() => dateProps.value.map((p) => ({ label: p.name, icon: 'calendar', checked: v.value?.config.date_prop === p.id, run: () => props.db.updateView({ date_prop: p.id }) })))

function toggleVisible(pid: string) {
  const all = props.db.props.value.map((p) => p.id)
  const cur = v.value?.config.visible?.length ? [...v.value.config.visible] : all
  const next = cur.includes(pid) ? cur.filter((x) => x !== pid) : [...cur, pid]
  props.db.updateView({ visible: next })
}
const isVisible = (pid: string) => !v.value?.config.visible?.length || v.value.config.visible.includes(pid)
</script>

<template>
  <div class="vb">
    <div class="tabs">
      <template v-for="vw in db.views.value" :key="vw.id">
        <Menu v-if="vw.id === v?.id && db.canEdit.value" :items="viewMenu(vw.id)">
          <button class="tab on"><Icon :name="VIEW_ICONS[vw.type]" :size="15" />{{ vw.name }}</button>
        </Menu>
        <button v-else class="tab" :class="{ on: vw.id === v?.id }" @click="db.viewId.value = vw.id; db.query()"><Icon :name="VIEW_ICONS[vw.type]" :size="15" />{{ vw.name }}</button>
      </template>
      <Menu v-if="db.canEdit.value" :items="addViewMenu"><button class="tab add"><Icon name="plus" :size="15" /></button></Menu>
    </div>
    <span class="ln-spacer" />
    <div class="tools">
      <template v-if="!publicMode">
      <PopoverRoot>
        <PopoverTrigger as-child><button class="tb" :class="{ on: filterCount }"><Icon name="filter" :size="15" /><span v-if="filterCount">{{ filterCount }}</span></button></PopoverTrigger>
        <PopoverPortal><PopoverContent class="db-pop" align="end" :side-offset="6"><FilterEditor :db="db" /></PopoverContent></PopoverPortal>
      </PopoverRoot>
      <PopoverRoot>
        <PopoverTrigger as-child><button class="tb" :class="{ on: sortCount }"><Icon name="sort" :size="15" /><span v-if="sortCount">{{ sortCount }}</span></button></PopoverTrigger>
        <PopoverPortal><PopoverContent class="db-pop" align="end" :side-offset="6"><SortEditor :db="db" /></PopoverContent></PopoverPortal>
      </PopoverRoot>
      <Menu v-if="v && ['table', 'board', 'list', 'gallery'].includes(v.type)" :items="groupMenu" align="end"><button class="tb" :class="{ on: v.config.group_by }" :title="t('db.groupBy')"><Icon name="group" :size="15" /></button></Menu>
      <Menu v-if="v && ['calendar', 'timeline'].includes(v.type)" :items="dateMenu" align="end"><button class="tb" :title="t('db.dateProp')"><Icon name="calendar" :size="15" /></button></Menu>
      <PopoverRoot>
        <PopoverTrigger as-child><button class="tb" :title="t('db.properties')"><Icon name="more" :size="15" /></button></PopoverTrigger>
        <PopoverPortal>
          <PopoverContent class="db-pop" align="end" :side-offset="6">
            <div class="plist">
              <label v-for="p in db.props.value" :key="p.id" class="prow"><Icon :name="TYPE_ICONS[p.type]" :size="14" /><span class="ln-ellipsis">{{ p.name }}</span><span class="ln-spacer" /><input type="checkbox" :checked="isVisible(p.id)" :disabled="p.type === 'title'" @change="toggleVisible(p.id)" /></label>
              <Btn v-if="db.canEdit.value" size="sm" variant="ghost" icon="plus" @click="emit('newProp')">{{ t('db.newProperty') }}</Btn>
            </div>
          </PopoverContent>
        </PopoverPortal>
      </PopoverRoot>
      </template>
      <input v-if="searching" v-model="db.search.value" class="search" :placeholder="t('common.search')" autofocus @input="db.requery(200)" @blur="!db.search.value && (searching = false)" />
      <button v-else class="tb" @click="searching = true"><Icon name="search" :size="15" /></button>
      <Btn v-if="db.canEdit.value" size="sm" variant="primary" @click="emit('newRow')">{{ t('common.create') }}</Btn>
    </div>
  </div>
</template>

<style scoped>
.vb { display: flex; align-items: center; gap: 8px; border-bottom: 1px solid var(--border); padding: 0 0 4px; margin-bottom: 4px; flex-wrap: wrap; }
.tabs { display: flex; gap: 2px; overflow-x: auto; min-width: 0; }
.tab { display: inline-flex; align-items: center; gap: 5px; border: 0; background: none; color: var(--text-2); height: 30px; padding: 0 8px; border-radius: 5px; font-size: 13px; white-space: nowrap; }
.tab:hover { background: var(--bg-hover); }
.tab.on { color: var(--text); font-weight: 500; box-shadow: inset 0 -2px 0 var(--text); border-radius: 0; }
.tab.add { color: var(--text-3); }
.tools { display: flex; align-items: center; gap: 2px; }
.tb { display: inline-flex; align-items: center; gap: 3px; height: 28px; padding: 0 7px; border: 0; background: none; color: var(--text-3); border-radius: 5px; font-size: 12.5px; }
.tb:hover { background: var(--bg-hover); color: var(--text); }
.tb.on { color: var(--accent); }
.search { height: 28px; width: 160px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; font-size: 13px; }
.plist { display: flex; flex-direction: column; gap: 2px; min-width: 230px; max-height: 360px; overflow: auto; }
.prow { display: flex; align-items: center; gap: 6px; padding: 4px 6px; border-radius: 5px; font-size: 13px; cursor: pointer; }
.prow:hover { background: var(--bg-hover); }
</style>

<style>
.db-pop { background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); padding: 10px; z-index: 1200; }
</style>
