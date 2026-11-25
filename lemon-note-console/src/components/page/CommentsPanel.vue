<script setup lang="ts">
// 评论侧栏：段落批注线程 + 页面讨论。始终挂载（关着时只负责加载数据、给正文里的批注上高亮）。
import { ref, computed, watch, nextTick, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Menu from '@/components/ui/Menu.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useSession } from '@/stores/session'
import { userName, useUsers } from '@/lib/users'
import { errorText } from '@/i18n'
import { formatRelative } from '@/lib/time'
import { realtime } from '@/lib/realtime'
import { confirmDialog } from '@/lib/confirm'

export interface CommentDraft { from: number; to: number; quote: string; blockId: string }
interface Comment { id: string; thread_id: string; body: string; created_at: number; created_by: string; updated_at: number }
interface Thread { id: string; page_id: string; block_id?: string; anchor: string; resolved_at?: number; resolved_by?: string; created_at: number; created_by: string; comments: Comment[] }

const open = defineModel<boolean>('open', { default: false })
const draft = defineModel<CommentDraft | null>('draft', { default: null })
const props = defineProps<{ pageId: string }>()
const emit = defineEmits<{ created: [d: CommentDraft, threadId: string]; deleted: [threadId: string]; focus: [threadId: string] }>()
const { t } = useI18n()
const toast = useToast()
const session = useSession()
useUsers()

const threads = ref<Thread[]>([])
const canComment = ref(false)
const canModerate = ref(false)
const showResolved = ref(false)
const active = ref('')
const draftBody = ref('')
const pageBody = ref('')
const replies = ref<Record<string, string>>({})
const editing = ref('')
const editBody = ref('')
const busy = ref(false)
const listEl = ref<HTMLElement>()

async function load() {
  try {
    const r = await api.get(`/api/pages/${props.pageId}/comments?resolved=1`)
    threads.value = r.threads || []
    canComment.value = r.can_comment
    canModerate.value = r.can_moderate
  } catch { /* 没权限或页面没了：保持空 */ }
}
watch(() => props.pageId, () => { threads.value = []; active.value = ''; load() }, { immediate: true })
const off = realtime.on('comments', ({ page_id }: { page_id: string }) => { if (page_id === props.pageId) load() })
onBeforeUnmount(off)

const openThreads = computed(() => threads.value.filter((x) => !x.resolved_at))
const shown = computed(() => (showResolved.value ? threads.value : openThreads.value))
const count = computed(() => openThreads.value.length)
defineExpose({ count, focusThread })

// 只给未解决线程的批注上高亮（已解决、已删除的标记保持原样不显示）
const highlightCss = computed(() => {
  const sel = openThreads.value.filter((x) => x.anchor).map((x) => `.ln-editor .ln-comment[data-thread="${CSS.escape(x.id)}"]`)
  const act = active.value && openThreads.value.some((x) => x.id === active.value) ? `.ln-editor .ln-comment[data-thread="${CSS.escape(active.value)}"]{background:rgba(255,212,0,.36)}` : ''
  return (sel.length ? sel.join(',') + '{background:rgba(255,212,0,.14);border-bottom:2px solid rgba(255,212,0,.6);cursor:pointer}' : '') + act
})
let styleEl: HTMLStyleElement | null = null
watch(highlightCss, (css) => {
  if (!styleEl) { styleEl = document.createElement('style'); document.head.appendChild(styleEl) }
  styleEl.textContent = css
}, { immediate: true })
onBeforeUnmount(() => styleEl?.remove())

async function focusThread(id: string) {
  const th = threads.value.find((x) => x.id === id)
  if (!th) return
  if (th.resolved_at) showResolved.value = true
  open.value = true
  active.value = id
  await nextTick()
  listEl.value?.querySelector(`[data-tid="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
}

watch(draft, async (d) => {
  if (!d) return
  open.value = true
  draftBody.value = ''
  await nextTick()
  ;(listEl.value?.querySelector('.draft textarea') as HTMLTextAreaElement | null)?.focus()
})

async function submitDraft() {
  const d = draft.value
  if (!d || !draftBody.value.trim()) return
  busy.value = true
  try {
    const r = await api.post(`/api/pages/${props.pageId}/comments`, { block_id: d.blockId, anchor: d.quote, body: draftBody.value })
    threads.value.push(r.thread)
    emit('created', d, r.thread.id)
    draft.value = null
    active.value = r.thread.id
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function submitPage() {
  if (!pageBody.value.trim()) return
  busy.value = true
  try {
    const r = await api.post(`/api/pages/${props.pageId}/comments`, { body: pageBody.value })
    threads.value.push(r.thread)
    pageBody.value = ''
    await nextTick()
    listEl.value?.scrollTo({ top: listEl.value.scrollHeight, behavior: 'smooth' })
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

async function reply(th: Thread) {
  const body = replies.value[th.id]
  if (!body?.trim()) return
  try {
    const r = await api.post(`/api/pages/${props.pageId}/comments`, { thread_id: th.id, body })
    replace(r.thread)
    replies.value[th.id] = ''
  } catch (e) { toast.error(errorText(e)) }
}

function replace(th: Thread) {
  const i = threads.value.findIndex((x) => x.id === th.id)
  if (i >= 0) threads.value[i] = th
  else threads.value.push(th)
}

async function resolve(th: Thread, v: boolean) {
  try { replace((await api.post(`/api/comment-threads/${th.id}/resolve`, { resolved: v })).thread) } catch (e) { toast.error(errorText(e)) }
}

async function remove(c: Comment) {
  if (!(await confirmDialog({ title: t('comments.deleteTitle'), message: c.body.slice(0, 80), danger: true }))) return
  try {
    const r = await api.del(`/api/comments/${c.id}`)
    if (r.thread_deleted) {
      threads.value = threads.value.filter((x) => x.id !== c.thread_id)
      emit('deleted', c.thread_id)
    } else load()
  } catch (e) { toast.error(errorText(e)) }
}

function startEdit(c: Comment) { editing.value = c.id; editBody.value = c.body }
async function saveEdit(c: Comment) {
  try {
    const r = await api.patch(`/api/comments/${c.id}`, { body: editBody.value })
    Object.assign(c, r.comment)
    editing.value = ''
  } catch (e) { toast.error(errorText(e)) }
}

function menu(c: Comment) {
  const mine = c.created_by === session.user?.id
  return [
    ...(mine ? [{ label: t('common.edit'), icon: 'text', run: () => startEdit(c) }] : []),
    ...(mine || canModerate.value ? [{ label: t('common.delete'), icon: 'trash', danger: true, run: () => remove(c) }] : []),
  ]
}
function keySend(e: KeyboardEvent, fn: () => void) { if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); fn() } }
function initial(id: string) { return (userName(id) || '?').slice(0, 1).toUpperCase() }
function pick(th: Thread) { active.value = th.id; if (th.anchor) emit('focus', th.id) }
</script>

<template>
  <aside v-show="open" class="cp">
    <header class="ch">
      <span class="ct">{{ t('comments.title') }}</span>
      <span class="ln-spacer" />
      <button class="flt" :class="{ on: showResolved }" @click="showResolved = !showResolved"><Icon name="check" :size="14" />{{ t('comments.showResolved') }}</button>
      <IconBtn icon="close" :size="16" :label="t('common.close')" @click="open = false" />
    </header>
    <div ref="listEl" class="cl">
      <div v-if="draft" class="card draft">
        <div class="quote">{{ draft.quote }}</div>
        <textarea v-model="draftBody" rows="3" :placeholder="t('comments.placeholder')" @keydown="keySend($event, submitDraft)" />
        <div class="row-end">
          <Btn size="sm" variant="ghost" @click="draft = null">{{ t('common.cancel') }}</Btn>
          <Btn size="sm" variant="primary" :loading="busy" :disabled="!draftBody.trim()" @click="submitDraft">{{ t('comments.send') }}</Btn>
        </div>
      </div>
      <div v-if="!shown.length && !draft" class="empty">
        <Icon name="comment" :size="26" />
        <span>{{ t('comments.empty') }}</span>
      </div>
      <div v-for="th in shown" :key="th.id" class="card" :class="{ act: active === th.id, done: th.resolved_at }" :data-tid="th.id" @click="pick(th)">
        <div class="th-head">
          <div v-if="th.anchor" class="quote">{{ th.anchor }}</div>
          <div v-else class="pg"><Icon name="page" :size="13" />{{ t('comments.pageComment') }}</div>
          <span class="ln-spacer" />
          <IconBtn v-if="canComment && !th.resolved_at" icon="check" :size="15" :label="t('comments.resolve')" @click.stop="resolve(th, true)" />
          <IconBtn v-if="canComment && th.resolved_at" icon="undo" :size="15" :label="t('comments.reopen')" @click.stop="resolve(th, false)" />
        </div>
        <div v-for="c in th.comments" :key="c.id" class="cm">
          <span class="av">{{ initial(c.created_by) }}</span>
          <div class="cb">
            <div class="meta"><span class="nm">{{ userName(c.created_by) }}</span><span class="tm">{{ formatRelative(c.created_at) }}<template v-if="c.updated_at > c.created_at + 1000"> · {{ t('comments.edited') }}</template></span>
              <span class="ln-spacer" />
              <Menu v-if="menu(c).length" :items="menu(c)" align="end"><IconBtn class="more" icon="more" :size="14" :label="t('common.more')" @click.stop /></Menu>
            </div>
            <template v-if="editing === c.id">
              <textarea v-model="editBody" rows="3" @keydown="keySend($event, () => saveEdit(c))" @click.stop />
              <div class="row-end"><Btn size="sm" variant="ghost" @click.stop="editing = ''">{{ t('common.cancel') }}</Btn><Btn size="sm" variant="primary" @click.stop="saveEdit(c)">{{ t('common.save') }}</Btn></div>
            </template>
            <div v-else class="body">{{ c.body }}</div>
          </div>
        </div>
        <div v-if="th.resolved_at" class="resolved"><Icon name="check" :size="13" />{{ t('comments.resolvedBy', { name: userName(th.resolved_by || '') }) }}</div>
        <div v-if="canComment" class="reply" @click.stop>
          <textarea v-model="replies[th.id]" rows="1" :placeholder="t('comments.reply')" @keydown="keySend($event, () => reply(th))" />
          <IconBtn v-if="replies[th.id]?.trim()" icon="send" :size="15" :label="t('comments.send')" @click="reply(th)" />
        </div>
      </div>
    </div>
    <footer v-if="canComment" class="cf">
      <textarea v-model="pageBody" rows="2" :placeholder="t('comments.addPage')" @keydown="keySend($event, submitPage)" />
      <Btn size="sm" variant="primary" :loading="busy" :disabled="!pageBody.trim()" @click="submitPage">{{ t('comments.send') }}</Btn>
    </footer>
  </aside>
</template>

<style scoped>
.cp { width: 340px; flex: none; border-left: 1px solid var(--border); background: var(--bg); display: flex; flex-direction: column; min-height: 0; }
.ch { display: flex; align-items: center; gap: 6px; height: 44px; padding: 0 8px 0 16px; border-bottom: 1px solid var(--border); flex: none; }
.ct { font-weight: 600; }
.flt { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-3); font-size: 12.5px; padding: 4px 6px; border-radius: 5px; }
.flt:hover { background: var(--bg-hover); }
.flt.on { color: var(--accent); }
.cl { flex: 1; overflow-y: auto; padding: 12px; display: flex; flex-direction: column; gap: 10px; }
.empty { margin: 40px auto; display: flex; flex-direction: column; align-items: center; gap: 10px; color: var(--text-3); font-size: 13px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 10px 12px; background: var(--bg-elev); display: flex; flex-direction: column; gap: 8px; cursor: default; }
.card.act { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.card.done { opacity: .75; }
.th-head { display: flex; align-items: flex-start; gap: 4px; }
.quote { flex: 1; min-width: 0; border-left: 3px solid rgba(255, 212, 0, .7); padding-left: 8px; color: var(--text-2); font-size: 12.5px; line-height: 1.5; display: -webkit-box; -webkit-line-clamp: 3; -webkit-box-orient: vertical; overflow: hidden; }
.pg { flex: 1; display: flex; align-items: center; gap: 4px; color: var(--text-3); font-size: 12px; }
.cm { display: flex; gap: 8px; }
.av { width: 24px; height: 24px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: flex; align-items: center; justify-content: center; font-size: 11.5px; font-weight: 600; flex: none; }
.cb { flex: 1; min-width: 0; }
.meta { display: flex; align-items: center; gap: 6px; min-height: 22px; }
.nm { font-size: 13px; font-weight: 500; }
.tm { font-size: 11.5px; color: var(--text-3); }
.more { opacity: 0; }
.cm:hover .more { opacity: 1; }
.body { font-size: 13.5px; line-height: 1.55; white-space: pre-wrap; word-break: break-word; }
.resolved { display: flex; align-items: center; gap: 4px; font-size: 12px; color: var(--success); }
.reply { display: flex; align-items: flex-end; gap: 4px; }
textarea { width: 100%; resize: vertical; min-height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 6px 8px; background: var(--bg); color: var(--text); font: inherit; font-size: 13.5px; line-height: 1.5; }
.reply textarea { resize: none; }
textarea:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.row-end { display: flex; justify-content: flex-end; gap: 6px; }
.cf { border-top: 1px solid var(--border); padding: 10px 12px; display: flex; flex-direction: column; gap: 8px; align-items: flex-end; flex: none; }
@media (max-width: 760px) {
  .cp { position: fixed; inset: 0; z-index: 40; width: 100%; border-left: 0; }
}
</style>
