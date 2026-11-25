<script setup lang="ts">
// 侧栏「收件箱」：分享给我、评论回复、@ 提及。未读数实时更新。
import { ref, onMounted, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import { api } from '@/api/client'
import { realtime } from '@/lib/realtime'
import { formatRelative } from '@/lib/time'
import { useUI } from '@/stores/ui'

interface Item { id: string; kind: string; page_id?: string; page_title: string; page_icon?: string; page_kind?: string; actor_name: string; detail: any; read_at?: number; created_at: number }

const { t } = useI18n()
const router = useRouter()
const ui = useUI()
const open = ref(false)
const items = ref<Item[]>([])
const unread = ref(0)

async function load() {
  try {
    const r = await api.get('/api/notifications?limit=50')
    items.value = r.items
    unread.value = r.unread
  } catch { /* 离线时保持原样 */ }
}
onMounted(load)
const off = realtime.on('notifications', load)
const offOpen = realtime.on('open', load)
onBeforeUnmount(() => { off(); offOpen() })

function text(it: Item) {
  switch (it.kind) {
    case 'share': return t('inbox.share', { name: it.actor_name, role: t('share.roles.' + (it.detail?.role || 'viewer')) })
    case 'comment': return t('inbox.comment', { name: it.actor_name })
    case 'mention': return t('inbox.mention', { name: it.actor_name })
  }
  return it.kind
}
async function go(it: Item) {
  open.value = false
  if (ui.isMobile) ui.toggleSidebar(false)
  if (!it.read_at) {
    it.read_at = Date.now()
    unread.value = Math.max(0, unread.value - 1)
    api.post('/api/notifications/read', { ids: [it.id] }).catch(() => {})
  }
  if (it.page_id) router.push(`/p/${it.page_id}`)
}
async function readAll() {
  await api.post('/api/notifications/read', { all: true }).catch(() => {})
  load()
}
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child>
      <button class="item"><Icon name="inbox" :size="17" /><span>{{ t('inbox.title') }}</span><span v-if="unread" class="cnt">{{ unread > 99 ? '99+' : unread }}</span></button>
    </PopoverTrigger>
    <PopoverPortal>
      <PopoverContent class="inbox-pop" side="right" align="start" :side-offset="8" :collision-padding="12">
        <div class="ih"><span>{{ t('inbox.title') }}</span><span class="ln-spacer" /><button v-if="unread" class="ra" @click="readAll">{{ t('inbox.readAll') }}</button></div>
        <div class="il">
          <div v-if="!items.length" class="empty"><Icon name="inbox" :size="24" /><span>{{ t('inbox.empty') }}</span></div>
          <button v-for="it in items" :key="it.id" class="ii" :class="{ unread: !it.read_at }" @click="go(it)">
            <span class="dot" />
            <div class="ib">
              <div class="it">{{ text(it) }}</div>
              <div class="ip"><PageIcon :icon="it.page_icon" :kind="it.page_kind" :size="14" /><span class="ln-ellipsis">{{ it.page_title || t('common.untitled') }}</span></div>
              <div v-if="it.kind === 'comment' && it.detail?.excerpt" class="ex">{{ it.detail.excerpt }}</div>
            </div>
            <span class="tm">{{ formatRelative(it.created_at) }}</span>
          </button>
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<style scoped>
/* 与侧栏 .item 一致（父组件的作用域样式传不到这里） */
.item { display: flex; align-items: center; gap: 8px; height: var(--row-h); padding: 0 8px; margin: 0 6px; border-radius: 5px; color: var(--text-2); border: 0; background: none; width: calc(100% - 12px); text-align: left; font: inherit; }
.item:hover, .item[data-state="open"] { background: var(--bg-hover); color: var(--text); }
.cnt { margin-left: auto; min-width: 18px; height: 18px; padding: 0 5px; border-radius: 9px; background: var(--accent); color: #fff; font-size: 11px; line-height: 18px; text-align: center; }
</style>

<style>
.inbox-pop { width: 360px; max-width: calc(100vw - 24px); max-height: 70vh; display: flex; flex-direction: column; background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); z-index: 1100; overflow: hidden; }
.inbox-pop .ih { display: flex; align-items: center; height: 42px; padding: 0 12px 0 16px; font-weight: 600; border-bottom: 1px solid var(--border); flex: none; }
.inbox-pop .ra { border: 0; background: none; color: var(--accent); font-size: 12.5px; padding: 4px 6px; border-radius: 5px; }
.inbox-pop .ra:hover { background: var(--bg-hover); }
.inbox-pop .il { overflow-y: auto; padding: 4px; }
.inbox-pop .empty { display: flex; flex-direction: column; align-items: center; gap: 8px; padding: 36px 0; color: var(--text-3); font-size: 13px; }
.inbox-pop .ii { display: flex; gap: 8px; width: 100%; border: 0; background: none; text-align: left; padding: 8px 10px; border-radius: var(--radius-m); color: var(--text); }
.inbox-pop .ii:hover { background: var(--bg-hover); }
.inbox-pop .dot { width: 7px; height: 7px; border-radius: 50%; margin-top: 6px; flex: none; background: transparent; }
.inbox-pop .ii.unread .dot { background: var(--accent); }
.inbox-pop .ib { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 3px; }
.inbox-pop .it { font-size: 13px; }
.inbox-pop .ip { display: flex; align-items: center; gap: 4px; font-size: 12.5px; color: var(--text-2); min-width: 0; }
.inbox-pop .ex { font-size: 12.5px; color: var(--text-3); border-left: 2px solid var(--border-strong); padding-left: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.inbox-pop .tm { font-size: 11.5px; color: var(--text-3); white-space: nowrap; }
</style>
