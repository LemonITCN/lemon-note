<script setup lang="ts">
import { computed } from 'vue'
import type { DbProperty, DbRow } from '@/lib/db'
import { formatNumber, dateText } from '@/lib/db'
import { useWorkspace } from '@/stores/workspace'
import { formatDate } from '@/lib/time'
import Icon from '@/components/icons/Icon.vue'
import { userName } from '@/lib/users'

const props = defineProps<{ prop: DbProperty; row: DbRow; compact?: boolean }>()
const ws = useWorkspace()
const raw = computed(() => props.row.props?.[props.prop.id])
const opts = computed(() => props.prop.options || [])
const opt = (id: string) => opts.value.find((o) => o.id === id)
const users = computed(() => (Array.isArray(raw.value) ? raw.value : []).map(userName))
const computedVal = computed(() => props.row.computed?.[props.prop.id])
</script>

<template>
  <span class="cv" :class="prop.type">
    <template v-if="prop.type === 'title'">{{ row.title || $t('common.untitled') }}</template>
    <template v-else-if="prop.type === 'select' || prop.type === 'status'">
      <span v-if="opt(raw)" class="tag" :class="'cb-' + (opt(raw)?.color || 'gray')"><span v-if="prop.type === 'status'" class="sdot" :class="'dot-' + (opt(raw)?.color || 'gray')" />{{ opt(raw)?.name }}</span>
    </template>
    <template v-else-if="prop.type === 'multi_select'">
      <span v-for="id in (raw || [])" :key="id" class="tag" :class="'cb-' + (opt(id)?.color || 'gray')">{{ opt(id)?.name }}</span>
    </template>
    <template v-else-if="prop.type === 'number'">{{ formatNumber(raw, prop.format) }}</template>
    <template v-else-if="prop.type === 'checkbox'"><span class="cbx" :class="{ on: raw === true }"><Icon v-if="raw === true" name="check" :size="11" :stroke="2.6" /></span></template>
    <template v-else-if="prop.type === 'date'">{{ dateText(raw) }}</template>
    <template v-else-if="prop.type === 'url'"><a v-if="raw" :href="raw" target="_blank" rel="noopener noreferrer" class="lnk" @click.stop>{{ raw }}</a></template>
    <template v-else-if="prop.type === 'email'"><a v-if="raw" :href="'mailto:' + raw" class="lnk" @click.stop>{{ raw }}</a></template>
    <template v-else-if="prop.type === 'person'"><span v-for="u in users" :key="u" class="person">{{ u }}</span></template>
    <template v-else-if="prop.type === 'files'"><span v-for="(f, i) in (raw || [])" :key="i" class="tag cb-gray"><Icon name="paperclip" :size="11" />{{ f.name }}</span></template>
    <template v-else-if="prop.type === 'relation'">
      <RouterLink v-for="(id, i) in (raw || [])" :key="id" :to="`/p/${id}`" class="rel" @click.stop>{{ (computedVal || [])[i] || ws.pages.get(id)?.title || '…' }}</RouterLink>
    </template>
    <template v-else-if="prop.type === 'created_time'">{{ formatDate(row.created_at, true) }}</template>
    <template v-else-if="prop.type === 'last_edited_time'">{{ formatDate(row.updated_at, true) }}</template>
    <template v-else-if="prop.type === 'created_by'"><span class="person">{{ userName(row.created_by) }}</span></template>
    <template v-else-if="prop.type === 'last_edited_by'"><span class="person">{{ userName(row.updated_by) }}</span></template>
    <template v-else-if="prop.type === 'formula' || prop.type === 'rollup' || prop.type === 'unique_id'">
      <template v-if="typeof computedVal === 'boolean'"><span class="cbx" :class="{ on: computedVal }"><Icon v-if="computedVal" name="check" :size="11" :stroke="2.6" /></span></template>
      <template v-else-if="Array.isArray(computedVal)">{{ computedVal.join(', ') }}</template>
      <template v-else-if="typeof computedVal === 'number'">{{ formatNumber(computedVal, prop.format) }}</template>
      <template v-else>{{ computedVal ?? '' }}</template>
    </template>
    <template v-else>{{ raw ?? '' }}</template>
  </span>
</template>

<style scoped>
.cv { display: inline-flex; flex-wrap: wrap; align-items: center; gap: 4px; min-width: 0; line-height: 1.4; }
.tag { display: inline-flex; align-items: center; gap: 4px; padding: 0 7px; height: 20px; border-radius: 4px; font-size: 12.5px; white-space: nowrap; color: var(--text); }
.sdot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.dot-gray { color: var(--c-gray); } .dot-blue { color: var(--c-blue); } .dot-green { color: var(--c-green); } .dot-yellow { color: var(--c-yellow); }
.dot-orange { color: var(--c-orange); } .dot-red { color: var(--c-red); } .dot-purple { color: var(--c-purple); } .dot-pink { color: var(--c-pink); } .dot-brown { color: var(--c-brown); }
.cbx { width: 16px; height: 16px; border: 1.5px solid var(--border-strong); border-radius: 4px; display: inline-flex; align-items: center; justify-content: center; }
.cbx.on { background: var(--accent); border-color: var(--accent); color: #fff; }
.lnk { color: inherit; text-decoration: underline; text-decoration-color: var(--border-strong); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rel { color: inherit; text-decoration: underline; text-decoration-color: var(--border-strong); text-underline-offset: 3px; }
.person { font-size: 13px; }
</style>
