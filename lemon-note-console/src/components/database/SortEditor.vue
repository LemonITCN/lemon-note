<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import Btn from '@/components/ui/Btn.vue'
import type { DB } from './useDatabase'

const props = defineProps<{ db: DB }>()
const { t } = useI18n()
const sorts = computed(() => props.db.view.value?.config.sorts || [])
function save(l: any[]) { props.db.updateView({ sorts: l.length ? l : undefined }) }
function add() { save([...sorts.value, { prop: props.db.props.value[0].id, dir: 'asc' }]) }
function patch(i: number, ch: any) { const l = sorts.value.map((s) => ({ ...s })); Object.assign(l[i], ch); save(l) }
function remove(i: number) { const l = sorts.value.slice(); l.splice(i, 1); save(l) }
</script>

<template>
  <div class="se">
    <div v-for="(s, i) in sorts" :key="i" class="row">
      <select class="sel" :value="s.prop" @change="patch(i, { prop: ($event.target as HTMLSelectElement).value })"><option v-for="p in db.props.value" :key="p.id" :value="p.id">{{ p.name }}</option></select>
      <select class="sel" :value="s.dir" @change="patch(i, { dir: ($event.target as HTMLSelectElement).value })"><option value="asc">{{ t('db.asc') }}</option><option value="desc">{{ t('db.desc') }}</option></select>
      <button class="x" @click="remove(i)"><Icon name="close" :size="13" /></button>
    </div>
    <Btn size="sm" variant="ghost" icon="plus" @click="add">{{ t('db.addSort') }}</Btn>
  </div>
</template>

<style scoped>
.se { display: flex; flex-direction: column; gap: 6px; min-width: 300px; }
.row { display: flex; gap: 6px; align-items: center; }
.sel { height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 6px; background: var(--bg); font-size: 12.5px; color: var(--text); flex: 1; }
.x { border: 0; background: none; color: var(--text-3); padding: 4px; display: flex; border-radius: 4px; }
</style>
