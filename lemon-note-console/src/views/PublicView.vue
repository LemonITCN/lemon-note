<script setup lang="ts">
// 公开分享页：匿名只读。左侧是分享范围内的页面树（含子页面时），右侧是正文。
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { api, ApiError } from '@/api/client'
import { pub } from '@/lib/publicCtx'
import { seedUsers } from '@/lib/users'
import { useUI } from '@/stores/ui'
import { formatRelative } from '@/lib/time'
import DocPreview from '@/components/editor/DocPreview.vue'
import DatabaseView from '@/components/database/DatabaseView.vue'
import RowProps from '@/components/database/RowProps.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import PageCover from '@/components/page/PageCover.vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'

const props = defineProps<{ token: string; pageId?: string }>()
const { t } = useI18n()
const router = useRouter()
const ui = useUI()

const info = ref<any>(null)
const state = ref<'loading' | 'locked' | 'gone' | 'ok'>('loading')
const page = ref<any>(null)
const pageErr = ref(false)
const pw = ref('')
const pwErr = ref('')
const unlocking = ref(false)
const navOpen = ref(false)
let robots: HTMLMetaElement | null = null

const currentId = computed(() => props.pageId || info.value?.page_id || '')
const hasTree = computed(() => (info.value?.tree?.length || 0) > 1)

async function loadInfo() {
  state.value = 'loading'
  try {
    info.value = await api.get(`/api/public/${props.token}`)
    setRobots(!!info.value.allow_index)
    if (info.value.needs_password) { state.value = 'locked'; return }
    pub.rootId = info.value.page_id
    pub.tree = info.value.tree || []
    state.value = 'ok'
    await loadPage()
  } catch (e) {
    state.value = 'gone'
    setRobots(false)
  }
}

async function loadPage() {
  if (!currentId.value) return
  pageErr.value = false
  try {
    const r = await api.get(`/api/pages/${currentId.value}`)
    seedUsers(r.users)
    page.value = r
    document.title = (r.page.title || t('common.untitled')) + (info.value?.site_name ? ' - ' + info.value.site_name : '')
  } catch (e) {
    if (e instanceof ApiError && e.code === 'share_password') { state.value = 'locked'; return }
    pageErr.value = true
    page.value = null
  }
  navOpen.value = false
}

function setRobots(allow: boolean) {
  if (!robots) { robots = document.createElement('meta'); robots.name = 'robots'; document.head.appendChild(robots) }
  robots.content = allow ? 'all' : 'noindex, nofollow'
}
onBeforeUnmount(() => { robots?.remove() })

async function unlock() {
  if (!pw.value) return
  unlocking.value = true
  pwErr.value = ''
  try {
    await api.post(`/api/public/${props.token}/unlock`, { password: pw.value })
    pw.value = ''
    await loadInfo()
  } catch (e) {
    pwErr.value = e instanceof ApiError && e.code === 'too_many_attempts' ? t('share.tooMany') : t('share.badPassword')
  } finally { unlocking.value = false }
}

watch(() => props.token, loadInfo, { immediate: true })
watch(() => props.pageId, () => { if (state.value === 'ok') loadPage() })

interface Node { id: string; title: string; icon?: string; kind: string; depth: number }
const flat = computed<Node[]>(() => {
  const items = pub.tree
  const kids = new Map<string, any[]>()
  for (const p of items) {
    const k = p.parent_id || ''
    if (!kids.has(k)) kids.set(k, [])
    kids.get(k)!.push(p)
  }
  for (const l of kids.values()) l.sort((a, b) => (a.position < b.position ? -1 : a.position > b.position ? 1 : 0))
  const out: Node[] = []
  const walk = (id: string, depth: number) => {
    const p = items.find((x) => x.id === id)
    if (!p) return
    out.push({ id: p.id, title: p.title, icon: p.icon, kind: p.kind, depth })
    for (const c of kids.get(id) || []) walk(c.id, depth + 1)
  }
  walk(pub.rootId, 0)
  return out
})

function go(id: string) { router.push({ name: 'share', params: { token: props.token, pageId: id === pub.rootId ? undefined : id } }) }
const blocks = computed(() => (page.value?.blocks || []).map((b: any) => b.data))
const p = computed(() => page.value?.page)
const flags = computed(() => page.value?.flags || {})
function toggleTheme() { ui.appearance.theme = ui.effectiveTheme === 'dark' ? 'light' : 'dark' }
</script>

<template>
  <div class="pubv">
    <div v-if="state === 'loading'" class="center"><span class="spin" /></div>
    <div v-else-if="state === 'gone'" class="center msg">
      <Icon name="link" :size="28" class="mi" />
      <div class="mt">{{ t('share.notFound') }}</div>
    </div>
    <form v-else-if="state === 'locked'" class="center gate" @submit.prevent="unlock">
      <Icon name="lock" :size="26" class="mi" />
      <div class="mt">{{ t('share.needPassword') }}</div>
      <input v-model="pw" type="password" class="pwi" autocomplete="current-password" :placeholder="t('share.password')" autofocus />
      <div v-if="pwErr" class="err">{{ pwErr }}</div>
      <Btn variant="primary" type="submit" block :loading="unlocking">{{ t('share.view') }}</Btn>
    </form>
    <template v-else>
      <aside v-if="hasTree" class="side" :class="{ open: navOpen }">
        <div class="sh">{{ info?.site_name }}</div>
        <nav class="tree">
          <button v-for="n in flat" :key="n.id" class="ti" :class="{ on: n.id === currentId }" :style="{ paddingLeft: 10 + n.depth * 16 + 'px' }" @click="go(n.id)">
            <PageIcon :icon="n.icon" :kind="n.kind" :size="16" /><span class="ln-ellipsis">{{ n.title || t('common.untitled') }}</span>
          </button>
        </nav>
      </aside>
      <div v-if="navOpen" class="scrim" @click="navOpen = false" />
      <main class="main">
        <header class="topbar">
          <IconBtn v-if="hasTree" class="menu" icon="menu" :label="t('nav.pages')" @click="navOpen = !navOpen" />
          <nav class="crumbs">
            <template v-for="c in page?.breadcrumbs || []" :key="c.id">
              <button class="crumb" @click="go(c.id)"><PageIcon :icon="c.icon" :kind="c.kind" :size="15" /><span class="ln-ellipsis">{{ c.title || t('common.untitled') }}</span></button>
              <span class="slash">/</span>
            </template>
            <span v-if="p" class="crumb cur"><PageIcon :icon="p.icon" :kind="p.kind" :size="15" /><span class="ln-ellipsis">{{ p.title || t('common.untitled') }}</span></span>
          </nav>
          <span class="ln-spacer" />
          <span v-if="p" class="edited">{{ t('page.lastEdited') }} {{ formatRelative(p.updated_at) }}</span>
          <IconBtn :icon="ui.effectiveTheme === 'dark' ? 'sun' : 'moon'" :label="t('settings.theme')" @click="toggleTheme" />
        </header>
        <div class="scroll">
          <div v-if="pageErr" class="center msg small"><div class="mt">{{ t('share.noAccess') }}</div></div>
          <template v-else-if="p">
            <PageCover :cover="p.cover" :editable="false" />
            <div class="doc" :class="{ wide: flags.full_width || p.kind === 'database', small: flags.small_text, ['font-' + (flags.font || 'default')]: true, 'has-cover': !!p.cover }">
              <div class="head">
                <div v-if="p.icon" class="big-icon"><PageIcon :icon="p.icon" :size="64" /></div>
                <h1 v-if="!flags.hide_title" class="title">{{ p.title || t('common.untitled') }}</h1>
              </div>
              <RowProps v-if="p.database_id" :database-id="p.database_id" :row-id="p.id" :editable="false" />
              <DatabaseView v-if="p.kind === 'database'" :key="p.id" :database-id="p.id" :editable="false" full />
              <DocPreview v-else :key="p.id" :blocks="blocks" :page-id="p.id" />
            </div>
            <footer class="foot">{{ t('share.sharedVia', { site: info?.site_name || 'Lemon Note' }) }}</footer>
          </template>
        </div>
      </main>
    </template>
  </div>
</template>

<style scoped>
.pubv { height: 100vh; height: 100dvh; display: flex; background: var(--bg); color: var(--text); }
.center { margin: auto; display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 24px; }
.msg .mt, .gate .mt { font-size: 15px; font-weight: 500; }
.msg.small { margin-top: 120px; color: var(--text-2); }
.mi { color: var(--text-3); }
.gate { width: 300px; max-width: calc(100vw - 32px); }
.pwi { width: 100%; height: 36px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); color: var(--text); }
.pwi:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.err { color: var(--danger); font-size: 13px; align-self: flex-start; }
.spin { width: 22px; height: 22px; border: 2px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: sp .8s linear infinite; }
@keyframes sp { to { transform: rotate(360deg); } }
.side { width: 260px; flex: none; background: var(--bg-soft); border-right: 1px solid var(--border); display: flex; flex-direction: column; overflow: hidden; }
.sh { height: 44px; display: flex; align-items: center; padding: 0 14px; font-weight: 600; font-size: 14px; flex: none; }
.tree { flex: 1; overflow-y: auto; padding: 4px 6px 16px; }
.ti { display: flex; align-items: center; gap: 6px; width: 100%; height: 30px; border: 0; background: none; border-radius: 5px; color: var(--text-2); text-align: left; padding-right: 8px; font-size: 13.5px; }
.ti:hover { background: var(--bg-hover); }
.ti.on { background: var(--bg-active); color: var(--text); font-weight: 500; }
.main { flex: 1; min-width: 0; display: flex; flex-direction: column; }
.topbar { display: flex; align-items: center; gap: 6px; height: 44px; padding: 0 10px 0 14px; flex: none; }
.menu { display: none; }
.crumbs { display: flex; align-items: center; gap: 2px; min-width: 0; overflow: hidden; }
.crumb { display: inline-flex; align-items: center; gap: 5px; padding: 3px 6px; border-radius: 4px; color: var(--text-2); border: 0; background: none; min-width: 0; max-width: 220px; font-size: 13.5px; }
button.crumb:hover { background: var(--bg-hover); color: var(--text); }
.crumb.cur { color: var(--text); }
.slash { color: var(--text-3); }
.edited { font-size: 12.5px; color: var(--text-3); white-space: nowrap; }
.scroll { flex: 1; overflow-y: auto; overflow-x: hidden; }
.doc { max-width: 760px; margin: 0 auto; padding: 0 56px; }
.doc.wide { max-width: none; padding: 0 72px; }
.doc.small { --content-size: 14px; }
.doc.font-serif { --font-content: var(--font-serif); }
.doc.font-mono { --font-content: var(--font-mono); }
.head { padding-top: 72px; }
.has-cover .head { padding-top: 0; }
.has-cover .big-icon { margin-top: -42px; position: relative; }
.big-icon { margin-bottom: 6px; padding: 4px; }
.title { font-size: 40px; font-weight: 700; line-height: 1.2; margin: 4px 0 12px; font-family: var(--font-content, var(--font-ui)); word-break: break-word; }
.foot { max-width: 760px; margin: 48px auto 32px; padding: 0 56px; font-size: 12.5px; color: var(--text-3); }
.scrim { display: none; }
@media (max-width: 760px) {
  .menu { display: inline-flex; }
  .side { position: fixed; left: 0; top: 0; bottom: 0; z-index: 30; transform: translateX(-100%); transition: transform .2s; box-shadow: var(--shadow-pop); }
  .side.open { transform: none; }
  .scrim { display: block; position: fixed; inset: 0; background: var(--overlay); z-index: 29; }
  .doc, .doc.wide, .foot { padding: 0 18px; }
  .head { padding-top: 32px; }
  .title { font-size: 30px; }
  .edited { display: none; }
}
</style>
