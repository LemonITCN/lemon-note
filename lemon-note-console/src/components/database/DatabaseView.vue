<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import ViewBar from './ViewBar.vue'
import TableView from './TableView.vue'
import BoardView from './BoardView.vue'
import GalleryView from './GalleryView.vue'
import CalendarView from './CalendarView.vue'
import TimelineView from './TimelineView.vue'
import DbChart from './DbChart.vue'
import PropertyEditor from './PropertyEditor.vue'
import Icon from '@/components/icons/Icon.vue'
import { useDatabase } from './useDatabase'
import type { DbRow } from '@/lib/db'

const props = defineProps<{ databaseId: string; editable?: boolean; full?: boolean; viewId?: string; inlineTitle?: boolean }>()
const { t } = useI18n()
const router = useRouter()
const db = useDatabase(props.databaseId, props.viewId)
const propOpen = ref(false)
const propId = ref<string | undefined>()
const type = computed(() => db.view.value?.type || 'table')
const groups = computed(() => db.result.value.groups)
const flatRows = computed<DbRow[]>(() => db.result.value.groups ? db.result.value.groups.flatMap((g) => g.rows) : db.result.value.rows)
const collapsed = ref(new Set<string>())

onMounted(() => db.load())

function editProp(id?: string) { propId.value = id; propOpen.value = true }

async function newRow() {
  const r = await db.addRow({})
  if (r && type.value !== 'table') router.push(`/p/${r.id}`)
}

function toggle(k: string) { const s = new Set(collapsed.value); s.has(k) ? s.delete(k) : s.add(k); collapsed.value = s }
</script>

<template>
  <div class="dbv" :class="{ full }">
    <div v-if="inlineTitle && db.page.value" class="itl" @click="router.push(`/p/${databaseId}`)"><Icon name="database" :size="16" />{{ db.page.value.title || t('common.untitled') }}</div>
    <p v-if="db.error.value" class="err">{{ db.error.value }}</p>
    <template v-else-if="!db.loading.value">
      <ViewBar :db="db" @new-row="newRow" @new-prop="editProp()" />
      <template v-if="type === 'board'">
        <BoardView v-if="groups" :db="db" :groups="groups" />
        <p v-else class="hint">{{ t('db.boardNeedsGroup') }}</p>
      </template>
      <CalendarView v-else-if="type === 'calendar'" :db="db" :rows="flatRows" />
      <TimelineView v-else-if="type === 'timeline'" :db="db" :rows="flatRows" />
      <DbChart v-else-if="type === 'chart'" :db="db" :db-id="databaseId" />
      <template v-else-if="groups">
        <div v-for="g in groups" :key="g.key" class="grp">
          <button class="gh" @click="toggle(g.key)"><Icon :name="collapsed.has(g.key) ? 'chevron-right' : 'chevron-down'" :size="14" /><span class="tag" :class="'cb-' + (g.color || 'gray')">{{ g.label || t('db.noValue') }}</span><span class="cnt">{{ g.count }}</span></button>
          <template v-if="!collapsed.has(g.key)">
            <TableView v-if="type === 'table'" :db="db" :rows="g.rows" @edit-prop="editProp" />
            <GalleryView v-else :db="db" :rows="g.rows" :list="type === 'list'" />
          </template>
        </div>
      </template>
      <TableView v-else-if="type === 'table'" :db="db" :rows="db.result.value.rows" @edit-prop="editProp" />
      <GalleryView v-else :db="db" :rows="db.result.value.rows" :list="type === 'list'" />
      <div class="count">{{ t('db.count') }} {{ db.result.value.total }}</div>
    </template>
    <PropertyEditor v-model:open="propOpen" :db="db" :prop-id="propId" />
  </div>
</template>

<style scoped>
.dbv { margin: 6px 0; }
.itl { display: inline-flex; align-items: center; gap: 6px; font-weight: 600; font-size: 16px; padding: 4px 2px 8px; cursor: pointer; }
.itl:hover { text-decoration: underline; }
.grp { margin-bottom: 10px; }
.gh { display: flex; align-items: center; gap: 6px; border: 0; background: none; padding: 6px 2px; color: var(--text-2); }
.tag { padding: 0 7px; height: 20px; display: inline-flex; align-items: center; border-radius: 4px; font-size: 12.5px; color: var(--text); }
.cnt { color: var(--text-3); font-size: 12px; }
.count { font-size: 12px; color: var(--text-3); padding: 6px 2px; }
.err, .hint { color: var(--text-3); font-size: 13px; }
</style>
