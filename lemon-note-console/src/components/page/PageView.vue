<script setup lang="ts">
import { ref, computed, onMounted, onBeforeUnmount, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api } from '@/api/client'
import type { PageResponse, PageFlags } from '@/api/types'
import { roleAtLeast } from '@/api/types'
import Editor from '@/components/editor/Editor.vue'
import type { SaveState } from '@/components/editor/blockSync'
import PageIcon from './PageIcon.vue'
import IconPicker from './IconPicker.vue'
import PageCover from './PageCover.vue'
import HistoryPanel from './HistoryPanel.vue'
import ShareDialog from './ShareDialog.vue'
import CommentsPanel, { type CommentDraft } from './CommentsPanel.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import DatabaseView from '@/components/database/DatabaseView.vue'
import RowProps from '@/components/database/RowProps.vue'
import { useWorkspace } from '@/stores/workspace'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { errorText } from '@/i18n'
import { realtime } from '@/lib/realtime'
import { formatRelative } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'
import { addToChat } from '@/lib/chatRefs'

const props = defineProps<{ id: string }>()
const { t } = useI18n()
const router = useRouter()
const ws = useWorkspace()
const ui = useUI()
const toast = useToast()
const data = ref<PageResponse | null>(null)
const error = ref('')
const title = ref('')
const saveState = ref<SaveState>('saved')
const historyOpen = ref(false)
const shareOpen = ref(false)
const commentsOpen = ref(false)
const commentDraft = ref<CommentDraft | null>(null)
const commentsRef = ref<InstanceType<typeof CommentsPanel>>()
const titleEl = ref<HTMLTextAreaElement>()
const editorRef = ref<InstanceType<typeof Editor>>()
let titleTimer: number | undefined

const page = computed(() => data.value?.page)
const flags = computed<PageFlags>(() => data.value?.flags || {})
const canEdit = computed(() => roleAtLeast(data.value?.role || '', 'editor') && !flags.value.locked && !page.value?.trashed_at)
const isDb = computed(() => page.value?.kind === 'database')

async function load() {
  try {
    const r = await api.get<PageResponse>(`/api/pages/${props.id}`)
    data.value = r
    title.value = r.page.title
    ws.upsert({ ...(ws.pages.get(r.page.id) || {}), ...r.page, parent_id: r.page.parent_id || undefined, database_id: r.page.database_id || undefined } as any)
    ws.revealPath(r.page.id)
    if (ws.currentSpaceId !== r.page.space_id && ws.spaces.some((s) => s.space.id === r.page.space_id)) ws.setSpace(r.page.space_id)
    api.post(`/api/pages/${props.id}/visit`).catch(() => {})
    document.title = (r.page.title || t('common.untitled')) + ' - ' + (document.title.split(' - ').pop() || 'Lemon Note')
    await nextTick()
    autosize()
    if (!r.page.title && canEdit.value && !r.blocks.length) titleEl.value?.focus()
  } catch (e: any) {
    error.value = e?.status === 404 ? t('page.notFound') : errorText(e)
  }
}

const offPage = realtime.on('page', ({ meta, origin }: any) => {
  if (meta.id !== props.id || !data.value) return
  if (document.activeElement !== titleEl.value) title.value = meta.title
  data.value.page.title = meta.title
  data.value.page.icon = meta.icon
  data.value.page.updated_at = meta.updated_at
  if (!origin && meta.trashed_at) data.value.page = { ...data.value.page, trashed_at: meta.trashed_at } as any
})

onMounted(() => {
  realtime.subscribe('page:' + props.id)
  load()
})
onBeforeUnmount(() => {
  realtime.unsubscribe('page:' + props.id)
  offPage()
  if (titleTimer) { clearTimeout(titleTimer); saveTitle() }
})

function autosize() {
  const el = titleEl.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = el.scrollHeight + 'px'
}

watch(title, () => {
  autosize()
  if (!data.value || title.value === data.value.page.title) return
  clearTimeout(titleTimer)
  titleTimer = window.setTimeout(saveTitle, 500)
})

async function saveTitle() {
  titleTimer = undefined
  if (!data.value || title.value === data.value.page.title) return
  try {
    const r = await api.patch(`/api/pages/${props.id}`, { title: title.value })
    data.value.page.title = r.page.title
    ws.upsert({ ...ws.pages.get(props.id)!, title: r.page.title })
  } catch (e) { toast.error(errorText(e)) }
}

function titleKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.isComposing) {
    e.preventDefault()
    editorRef.value?.editor?.commands.focus('start')
  }
}

async function patch(body: Record<string, any>) {
  try {
    const r = await api.patch(`/api/pages/${props.id}`, body)
    if (!data.value) return
    data.value.page = r.page
    data.value.flags = r.flags
    ws.upsert({ ...ws.pages.get(props.id)!, title: r.page.title, icon: r.page.icon })
  } catch (e) { toast.error(errorText(e)) }
}

function setFlag(k: keyof PageFlags, v: any) { patch({ flags: { ...flags.value, [k]: v } }) }

async function createSubpage() {
  try {
    const m = await ws.createPage({ spaceId: page.value!.space_id, parentId: props.id })
    editorRef.value?.editor?.chain().focus().insertContent({ type: 'childPage', attrs: { pageId: m.id } }).run()
    await editorRef.value?.flush()
    router.push(`/p/${m.id}`)
  } catch (e) { toast.error(errorText(e)) }
}

async function createDatabase() {
  try {
    const r = await api.post('/api/databases', { space_id: page.value!.space_id, parent_id: props.id, title: '', inline: true })
    editorRef.value?.editor?.chain().focus().insertContent({ type: 'childDatabase', attrs: { pageId: r.page.id, viewId: r.views?.[0]?.id || '' } }).run()
  } catch (e) { toast.error(errorText(e)) }
}

const moreMenu = computed(() => {
  const f = flags.value
  const items: any[] = [
    { label: t('page.fullWidth'), icon: 'full-width', checked: !!f.full_width, run: () => setFlag('full_width', !f.full_width) },
    { label: t('page.smallText'), icon: 'small-text', checked: !!f.small_text, run: () => setFlag('small_text', !f.small_text) },
    { heading: t('page.font') },
    { label: t('page.fontDefault'), icon: 'font', checked: !f.font || f.font === 'default', run: () => setFlag('font', 'default') },
    { label: t('page.fontSerif'), icon: 'font', checked: f.font === 'serif', run: () => setFlag('font', 'serif') },
    { label: t('page.fontMono'), icon: 'font', checked: f.font === 'mono', run: () => setFlag('font', 'mono') },
    { separator: true },
    { label: f.locked ? t('page.unlock') : t('page.lock'), icon: f.locked ? 'unlock' : 'lock', disabled: !roleAtLeast(data.value?.role || '', 'editor'), run: () => setFlag('locked', !f.locked) },
    { label: t('page.history'), icon: 'history', run: () => (historyOpen.value = true) },
    { label: t('editor.addToChat'), icon: 'assistant', run: () => addToChat({ kind: isDb.value ? 'database' : 'page', id: props.id, title: title.value, icon: page.value?.icon }) },
    ...(ui.isMobile ? [{ label: t('page.share'), icon: 'share', run: () => (shareOpen.value = true) }] : []),
    { label: t('page.copyLink'), icon: 'link', run: () => { navigator.clipboard?.writeText(location.origin + '/p/' + props.id); toast.success(t('common.copied')) } },
    { label: t('page.exportMarkdown'), icon: 'download', run: () => window.open(`/api/pages/${props.id}/export?format=md`, '_blank') },
  ]
  if (canEdit.value) {
    items.push({ label: t('page.duplicate'), icon: 'duplicate', run: async () => { const m = await ws.duplicatePage(props.id); router.push(`/p/${m.id}`) } })
    items.push({ separator: true }, { label: t('page.moveToTrash'), icon: 'trash', danger: true, run: trash })
  }
  return items
})

async function trash() {
  if (!(await confirmDialog({ title: t('page.moveToTrash'), message: title.value || t('common.untitled'), danger: true, okText: t('page.moveToTrash') }))) return
  try { await ws.trashPage(props.id); router.push('/') } catch (e) { toast.error(errorText(e)) }
}

async function restore() {
  try { await api.post(`/api/pages/${props.id}/restore`); await ws.loadTree(page.value!.space_id, true); error.value = ''; data.value = null; await load() } catch (e) { toast.error(errorText(e)) }
}

async function purge() {
  if (!(await confirmDialog({ title: t('page.deleteForever'), message: t('trash.confirmDelete'), danger: true, okText: t('page.deleteForever') }))) return
  try { await api.del(`/api/trash/${props.id}`); router.push('/') } catch (e) { toast.error(errorText(e)) }
}

const saveLabel = computed(() => ({ saved: '', saving: t('common.loading'), error: t('editor.uploadFailed'), offline: t('errors.network') })[saveState.value])
</script>

<template>
  <div class="pv">
    <header class="topbar">
      <nav class="crumbs">
        <template v-for="c in data?.breadcrumbs || []" :key="c.id">
          <RouterLink :to="`/p/${c.id}`" class="crumb"><PageIcon :icon="c.icon" :kind="c.kind" :size="15" /><span class="ln-ellipsis">{{ c.title || t('common.untitled') }}</span></RouterLink>
          <span class="slash">/</span>
        </template>
        <span v-if="page" class="crumb cur"><PageIcon :icon="page.icon" :kind="page.kind" :size="15" /><span class="ln-ellipsis">{{ title || t('common.untitled') }}</span></span>
        <Icon v-if="flags.locked" name="lock" :size="14" class="ln-faint" />
      </nav>
      <span class="ln-spacer" />
      <span v-if="saveLabel" class="save" :class="saveState">{{ saveLabel }}</span>
      <span v-if="page" class="edited">{{ t('page.lastEdited') }} {{ formatRelative(page.updated_at) }}</span>
      <template v-if="page">
        <Btn v-if="!ui.isMobile" size="sm" variant="ghost" @click="shareOpen = true">{{ t('page.share') }}</Btn>
        <span class="cbtn"><IconBtn icon="comment" :label="t('comments.title')" :active="commentsOpen" @click="commentsOpen = !commentsOpen" /><span v-if="commentsRef?.count" class="badge">{{ commentsRef.count }}</span></span>
        <IconBtn :icon="ws.isFavorite(id) ? 'star-filled' : 'star'" :label="ws.isFavorite(id) ? t('page.unfavorite') : t('page.favorite')" :class="{ fav: ws.isFavorite(id) }" @click="ws.setFavorite(id, !ws.isFavorite(id))" />
        <IconBtn icon="history" :label="t('page.history')" @click="historyOpen = true" />
        <Menu :items="moreMenu" align="end" :width="230"><IconBtn icon="more" :label="t('common.more')" /></Menu>
      </template>
    </header>
    <div v-if="page?.trashed_at" class="trash-banner">
      <span>{{ t('page.trashed') }}</span>
      <Btn size="sm" @click="restore">{{ t('page.restore') }}</Btn>
      <Btn size="sm" variant="danger" @click="purge">{{ t('page.deleteForever') }}</Btn>
    </div>
    <div class="body">
    <div class="scroll">
      <div v-if="error" class="err">{{ error }}</div>
      <template v-else-if="data && page">
        <PageCover :cover="page.cover" :editable="canEdit" @change="(v: string) => patch({ cover: v })" />
        <div class="doc" :class="{ wide: flags.full_width || isDb, small: flags.small_text, ['font-' + (flags.font || 'default')]: true, 'has-cover': !!page.cover }">
          <div class="head">
            <div v-if="page.icon" class="big-icon">
              <IconPicker :disabled="!canEdit" removable @pick="(v: string) => patch({ icon: v })"><button class="icon-btn-big"><PageIcon :icon="page.icon" :size="64" /></button></IconPicker>
            </div>
            <div v-if="canEdit" class="head-actions">
              <IconPicker v-if="!page.icon" @pick="(v: string) => patch({ icon: v })"><button class="ha"><Icon name="sparkle" :size="15" />{{ t('page.addIcon') }}</button></IconPicker>
              <button v-if="!page.cover" class="ha" @click="patch({ cover: 'gradient:1' })"><Icon name="image" :size="15" />{{ t('page.addCover') }}</button>
            </div>
            <textarea ref="titleEl" v-model="title" class="title" rows="1" :placeholder="t('page.untitled')" :readonly="!canEdit" @keydown="titleKey" @blur="saveTitle" />
          </div>
          <RowProps v-if="page.database_id" :database-id="page.database_id" :row-id="id" :editable="canEdit" />
          <DatabaseView v-if="isDb" :database-id="id" :editable="canEdit" full />
          <Editor v-else ref="editorRef" :key="id" :page-id="id" :space-id="page.space_id" :blocks="data.blocks" :editable="canEdit" :page-title="title"
            @state="(s: SaveState) => (saveState = s)" @create-subpage="createSubpage" @create-database="createDatabase"
            @comment="(d) => (commentDraft = d)" @open-thread="(tid: string) => commentsRef?.focusThread(tid)" />
        </div>
      </template>
      <div v-else class="loading" />
    </div>
    <CommentsPanel v-if="page" ref="commentsRef" :key="id" v-model:open="commentsOpen" v-model:draft="commentDraft" :page-id="id"
      @created="(d: CommentDraft, tid: string) => editorRef?.applyComment(d, tid)" @deleted="(tid: string) => editorRef?.removeComment(tid)" @focus="(tid: string) => editorRef?.revealComment(tid)" />
    </div>
    <HistoryPanel v-if="page" v-model:open="historyOpen" :page-id="id" :editable="canEdit" @restored="data = null; load()" />
    <ShareDialog v-if="page" v-model:open="shareOpen" :page-id="id" :page-title="title" :role="data?.role || ''" />
  </div>
</template>

<style scoped>
.pv { height: 100%; display: flex; flex-direction: column; min-width: 0; }
.topbar { display: flex; align-items: center; gap: 6px; height: 44px; padding: 0 10px 0 14px; flex: none; }
.crumbs { display: flex; align-items: center; gap: 2px; min-width: 0; overflow: hidden; }
.crumb { display: inline-flex; align-items: center; gap: 5px; padding: 3px 6px; border-radius: 4px; color: var(--text-2); text-decoration: none; min-width: 0; max-width: 220px; font-size: 13.5px; }
a.crumb:hover { background: var(--bg-hover); color: var(--text); }
.crumb.cur { color: var(--text); }
.slash { color: var(--text-3); }
.edited, .save { font-size: 12.5px; color: var(--text-3); white-space: nowrap; }
.save.error, .save.offline { color: var(--danger); }
.fav { color: var(--c-yellow) !important; }
.trash-banner { display: flex; align-items: center; justify-content: center; gap: 10px; background: var(--danger); color: #fff; padding: 6px 12px; font-size: 13.5px; }
.body { flex: 1; display: flex; min-height: 0; }
.scroll { flex: 1; overflow-y: auto; overflow-x: hidden; min-width: 0; }
.cbtn { position: relative; display: inline-flex; }
.badge { position: absolute; top: 1px; right: 0; min-width: 14px; height: 14px; padding: 0 3px; border-radius: 7px; background: var(--accent); color: #fff; font-size: 10px; line-height: 14px; text-align: center; pointer-events: none; }
.doc { max-width: 760px; margin: 0 auto; padding: 0 56px; }
.doc.wide { max-width: none; padding: 0 72px; }
.doc.small { --content-size: 14px; }
.doc.font-serif { --font-content: var(--font-serif); }
.doc.font-mono { --font-content: var(--font-mono); }
.head { padding-top: 72px; position: relative; }
.has-cover .head { padding-top: 0; }
.has-cover .big-icon { margin-top: -42px; }
.big-icon { margin-bottom: 6px; }
.icon-btn-big { border: 0; background: none; padding: 4px; border-radius: 8px; display: flex; }
.icon-btn-big:hover { background: var(--bg-hover); }
.head-actions { display: flex; gap: 4px; opacity: 0; transition: opacity .15s; margin-bottom: 4px; min-height: 28px; }
.head:hover .head-actions { opacity: 1; }
.ha { display: inline-flex; align-items: center; gap: 5px; border: 0; background: none; color: var(--text-3); font-size: 13px; padding: 4px 8px; border-radius: 5px; }
.ha:hover { background: var(--bg-hover); color: var(--text-2); }
.title { display: block; width: 100%; border: 0; outline: none; resize: none; background: none; font-family: var(--font-content); font-size: 40px; font-weight: 700; line-height: 1.2;
  color: var(--text); padding: 4px 2px; margin: 0 0 12px; overflow: hidden; letter-spacing: -.02em; }
.title::placeholder { color: var(--text-3); opacity: .6; }
.err { padding: 80px 20px; text-align: center; color: var(--text-2); }
.loading { height: 200px; }
@media (max-width: 760px) {
  .doc, .doc.wide { padding: 0 18px; }
  .head { padding-top: 28px; }
  .title { font-size: 30px; }
  .edited { display: none; }
}
</style>
