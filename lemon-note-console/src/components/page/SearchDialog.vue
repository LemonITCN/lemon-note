<script setup lang="ts">
import { ref, computed, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import PageIcon from './PageIcon.vue'
import { useUI } from '@/stores/ui'
import { useWorkspace } from '@/stores/workspace'
import { api } from '@/api/client'
import type { PageMeta } from '@/api/types'
import { formatRelative } from '@/lib/time'

const { t } = useI18n()
const ui = useUI()
const ws = useWorkspace()
const router = useRouter()
const q = ref('')
const input = ref<HTMLInputElement>()
const sel = ref(0)
const remote = ref<{ meta: PageMeta; snippet?: string }[]>([])
let timer: number | undefined

const local = computed(() => {
  const s = q.value.trim().toLowerCase()
  const all = [...ws.pages.values()].filter((p) => !p.trashed_at)
  if (!s) return all.sort((a, b) => b.updated_at - a.updated_at).slice(0, 12).map((meta) => ({ meta }))
  return all.filter((p) => p.title.toLowerCase().includes(s)).slice(0, 20).map((meta) => ({ meta }))
})
const results = computed(() => {
  const seen = new Set<string>()
  const out: { meta: PageMeta; snippet?: string }[] = []
  for (const r of [...local.value, ...remote.value]) {
    if (!seen.has(r.meta.id)) { seen.add(r.meta.id); out.push(r) }
  }
  return out.slice(0, 40)
})

watch(() => ui.searchOpen, async (v) => {
  if (v) { q.value = ''; sel.value = 0; remote.value = []; await nextTick(); input.value?.focus() }
})
watch(q, (v) => {
  sel.value = 0
  clearTimeout(timer)
  if (!v.trim()) { remote.value = []; return }
  timer = window.setTimeout(async () => {
    try { remote.value = (await api.get(`/api/search?q=${encodeURIComponent(v)}&limit=20`)).results || [] } catch { remote.value = [] }
  }, 180)
})

function go(r: { meta: PageMeta }) {
  ui.searchOpen = false
  router.push(`/p/${r.meta.id}`)
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'ArrowDown') { e.preventDefault(); sel.value = Math.min(sel.value + 1, results.value.length - 1) }
  else if (e.key === 'ArrowUp') { e.preventDefault(); sel.value = Math.max(sel.value - 1, 0) }
  else if (e.key === 'Enter' && results.value[sel.value]) { e.preventDefault(); go(results.value[sel.value]) }
}

function path(p: PageMeta) {
  const names: string[] = []
  let cur = p.parent_id ? ws.pages.get(p.parent_id) : undefined
  let guard = 0
  while (cur && guard++ < 6) { names.unshift(cur.title || t('common.untitled')); cur = cur.parent_id ? ws.pages.get(cur.parent_id) : undefined }
  return names.join(' / ')
}
</script>

<template>
  <DialogRoot v-model:open="ui.searchOpen">
    <DialogPortal>
      <DialogOverlay class="sd-overlay" />
      <DialogContent class="sd" :aria-describedby="undefined">
        <DialogTitle class="sr">{{ t('nav.search') }}</DialogTitle>
        <div class="bar">
          <Icon name="search" :size="18" />
          <input ref="input" v-model="q" :placeholder="t('nav.search')" @keydown="onKey" />
          <span class="ln-kbd">Esc</span>
        </div>
        <div class="results">
          <div v-for="(r, i) in results" :key="r.meta.id" class="res" :class="{ sel: i === sel }" @mouseenter="sel = i" @click="go(r)">
            <PageIcon :icon="r.meta.icon" :kind="r.meta.kind" :size="18" />
            <div class="txt">
              <div class="t ln-ellipsis">{{ r.meta.title || t('common.untitled') }}<span v-if="path(r.meta)" class="p"> · {{ path(r.meta) }}</span></div>
              <div v-if="r.snippet" class="snip ln-ellipsis" v-html="r.snippet" />
            </div>
            <span class="time">{{ formatRelative(r.meta.updated_at) }}</span>
          </div>
          <p v-if="!results.length" class="empty">{{ t('editor.noResults') }}</p>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style>
.sd-overlay { position: fixed; inset: 0; background: var(--overlay); z-index: 1000; }
.sd { position: fixed; top: 12vh; left: 50%; transform: translateX(-50%); width: 640px; max-width: calc(100vw - 24px); background: var(--bg-elev); border-radius: 12px; box-shadow: var(--shadow-pop); z-index: 1001; overflow: hidden; outline: none; }
.sd .sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); }
.sd .bar { display: flex; align-items: center; gap: 10px; padding: 0 16px; height: 52px; border-bottom: 1px solid var(--border); color: var(--text-3); }
.sd .bar input { flex: 1; border: 0; outline: none; background: none; font-size: 16px; color: var(--text); }
.sd .results { max-height: 56vh; overflow: auto; padding: 6px; }
.sd .res { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 6px; cursor: pointer; }
.sd .res.sel { background: var(--bg-hover); }
.sd .txt { flex: 1; min-width: 0; }
.sd .t { font-weight: 500; }
.sd .p { color: var(--text-3); font-weight: 400; font-size: 12.5px; }
.sd .snip { font-size: 12.5px; color: var(--text-2); margin-top: 2px; }
.sd .snip mark { background: var(--cb-yellow); color: inherit; border-radius: 2px; }
.sd .time { font-size: 12px; color: var(--text-3); flex: none; }
.sd .empty { text-align: center; color: var(--text-3); padding: 24px; }
</style>
