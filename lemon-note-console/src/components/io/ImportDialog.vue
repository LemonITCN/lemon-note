<script setup lang="ts">
// 导入：Notion 导出包 / Notion API / Obsidian 库 / Markdown / CSV。上传后在后台执行，这里轮询进度并显示报告。
import { ref, computed, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import { api, ApiError, getCsrf, getConnId } from '@/api/client'
import type { PageMeta } from '@/api/types'
import { useWorkspace } from '@/stores/workspace'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatSize } from '@/lib/files'

type Source = 'notion_zip' | 'notion_api' | 'obsidian' | 'markdown' | 'csv'
interface Warning { code: string; path?: string; detail?: string; message: string }
interface Report { pages: number; databases: number; rows: number; files: number; root_ids: string[]; warnings: Warning[]; omitted_warnings?: number; error?: string }
interface Job { id: string; kind: string; status: 'queued' | 'running' | 'done' | 'failed'; progress: { phase: string; done: number; total: number; current?: string }; report?: Report }

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ parentId?: string }>()
const { t, te } = useI18n()
const router = useRouter()
const ws = useWorkspace()
const toast = useToast()

const sources: { value: Source; icon: string; accept: string }[] = [
  { value: 'notion_zip', icon: 'archive', accept: '.zip' },
  { value: 'notion_api', icon: 'plug', accept: '' },
  { value: 'obsidian', icon: 'graph', accept: '.zip' },
  { value: 'markdown', icon: 'markdown', accept: '.md,.markdown,.zip' },
  { value: 'csv', icon: 'table', accept: '.csv,.zip' },
]

const source = ref<Source>('notion_zip')
const file = ref<File | null>(null)
const token = ref('')
const rootPage = ref('')
const targetId = ref('')
const spaceId = ref('')
const busy = ref(false)
const uploadPct = ref(-1)
const job = ref<Job | null>(null)
const dragging = ref(false)
const pickerOpen = ref(false)
const query = ref('')
let timer: number | undefined
let xhr: XMLHttpRequest | null = null

const current = computed(() => sources.find((s) => s.value === source.value)!)
const space = computed(() => ws.spaces.find((s) => s.space.id === spaceId.value)?.space || ws.currentSpace?.space)
const target = computed(() => (targetId.value ? ws.pages.get(targetId.value) : undefined))
const step = computed(() => (job.value ? (job.value.status === 'done' || job.value.status === 'failed' ? 'result' : 'running') : uploadPct.value >= 0 ? 'running' : 'form'))
const ready = computed(() => (source.value === 'notion_api' ? token.value.trim().length > 0 : !!file.value) && !!spaceId.value)

watch(open, (v) => {
  if (v && step.value === 'result') reset()
  if (!v || step.value !== 'form') return
  spaceId.value = ws.currentSpace?.space.id || ''
  const p = props.parentId ? ws.pages.get(props.parentId) : undefined
  targetId.value = p && p.kind !== 'database' && !p.database_id && p.space_id === spaceId.value ? p.id : ''
}, { immediate: true })

watch(source, () => { file.value = null })

// 目标位置：当前空间里的普通页面（数据库与数据库行不能作为导入目标）
const options = computed(() => {
  const sid = spaceId.value
  const out: { page: PageMeta; depth: number }[] = []
  const q = query.value.trim().toLowerCase()
  const walk = (parent: string | undefined, depth: number) => {
    for (const p of ws.children(sid, parent)) {
      if (p.kind === 'database') continue
      if (!q || (p.title || '').toLowerCase().includes(q)) out.push({ page: p, depth: q ? 0 : depth })
      if (out.length > 400) return
      walk(p.id, depth + 1)
    }
  }
  walk(undefined, 0)
  return out
})

function pick(id: string) { targetId.value = id; pickerOpen.value = false; query.value = '' }

function choose() {
  const input = document.createElement('input')
  input.type = 'file'
  input.accept = current.value.accept
  input.onchange = () => { if (input.files?.[0]) file.value = input.files[0] }
  input.click()
}

function onDrop(e: DragEvent) {
  dragging.value = false
  const f = e.dataTransfer?.files?.[0]
  if (f) file.value = f
}

// 上传用 XMLHttpRequest，才能显示上传进度（导入包可能有几个 GB）
function upload(fd: FormData): Promise<{ job: Job }> {
  return new Promise((resolve, reject) => {
    const x = new XMLHttpRequest()
    xhr = x
    x.open('POST', '/api/import')
    x.setRequestHeader('Accept', 'application/json')
    if (getCsrf()) x.setRequestHeader('X-CSRF-Token', getCsrf())
    if (getConnId()) x.setRequestHeader('X-Lemon-Conn', getConnId())
    x.upload.onprogress = (e) => { if (e.lengthComputable) uploadPct.value = Math.min(99, Math.round((e.loaded / e.total) * 100)) }
    x.onload = () => {
      xhr = null
      let data: any = null
      try { data = JSON.parse(x.responseText) } catch { /* 非 JSON 响应 */ }
      if (x.status >= 200 && x.status < 300 && data?.job) resolve(data)
      else reject(new ApiError(x.status, data?.error?.code || 'http_' + x.status, data?.error?.message || `HTTP ${x.status}`))
    }
    x.onerror = () => { xhr = null; reject(new ApiError(0, 'network', t('errors.network'))) }
    x.onabort = () => { xhr = null; reject(new ApiError(0, 'aborted', '')) }
    x.send(fd)
  })
}

async function start() {
  if (!ready.value || busy.value) return
  busy.value = true
  try {
    let r: { job: Job }
    if (source.value === 'notion_api') {
      uploadPct.value = 100
      r = await api.post('/api/import/notion-api', { token: token.value.trim(), root_page_id: rootPage.value.trim(), space_id: spaceId.value, parent_id: targetId.value })
      token.value = '' // 密钥只交给服务端这一次
    } else {
      // 字段必须在文件之前：服务端按顺序读，读到文件时要先知道来源与目标
      const fd = new FormData()
      fd.append('kind', source.value)
      fd.append('space_id', spaceId.value)
      fd.append('parent_id', targetId.value)
      fd.append('file', file.value!, file.value!.name)
      uploadPct.value = 0
      r = await upload(fd)
    }
    job.value = r.job
    poll()
  } catch (e: any) {
    uploadPct.value = -1
    if (e?.code !== 'aborted') toast.error(errorText(e))
  } finally { busy.value = false }
}

function poll() {
  clearTimeout(timer)
  timer = window.setTimeout(async () => {
    if (!job.value) return
    try {
      const r = await api.get<{ job: Job }>(`/api/import/${job.value.id}`)
      job.value = r.job
    } catch { /* 网络抖动：下次再试 */ }
    if (job.value && (job.value.status === 'done' || job.value.status === 'failed')) finished()
    else poll()
  }, 1000)
}

async function finished() {
  const j = job.value!
  if (spaceId.value) await ws.loadTree(spaceId.value, true).catch(() => {})
  if (!open.value) {
    if (j.status === 'done') toast.success(t('io.doneToast'))
    else toast.error(t('io.failedToast') + (j.report?.error ? '：' + j.report.error : ''))
  }
}

function reset() {
  clearTimeout(timer)
  job.value = null
  uploadPct.value = -1
  file.value = null
  token.value = ''
  rootPage.value = ''
}

function cancelUpload() { xhr?.abort() }

function close() {
  open.value = false
  if (step.value === 'result') reset()
}

function openResult() {
  const id = job.value?.report?.root_ids?.[0]
  close()
  if (id) router.push(`/p/${id}`)
}

onBeforeUnmount(() => { clearTimeout(timer); xhr?.abort() })

const phaseText = computed(() => {
  if (!job.value) return uploadPct.value >= 100 || source.value === 'notion_api' ? t('io.phase.queued') : t('io.uploading', { pct: Math.max(0, uploadPct.value) })
  return t('io.phase.' + (job.value.progress.phase || 'queued'))
})
const pct = computed(() => {
  if (!job.value) return uploadPct.value >= 0 && source.value !== 'notion_api' ? uploadPct.value : -1
  const p = job.value.progress
  return p.phase === 'creating' && p.total > 0 ? Math.min(100, Math.round((p.done / p.total) * 100)) : -1
})
const report = computed(() => job.value?.report)
const counts = computed(() => {
  const r = report.value
  if (!r) return []
  return [
    { key: 'pages', n: r.pages, icon: 'page' }, { key: 'databases', n: r.databases, icon: 'database' },
    { key: 'rows', n: r.rows, icon: 'list' }, { key: 'files', n: r.files, icon: 'paperclip' },
  ].filter((c) => c.n > 0)
})

function warnText(w: Warning) {
  const key = 'io.warn.' + w.code
  return te(key) ? t(key, { detail: w.detail || '' }) : w.message
}
</script>

<template>
  <Modal v-model:open="open" :title="t('io.importTitle')" :width="540">
    <!-- 表单 -->
    <div v-if="step === 'form'" class="form">
      <section>
        <div class="label">{{ t('io.source') }}</div>
        <div class="sources" role="radiogroup">
          <button v-for="s in sources" :key="s.value" type="button" role="radio" class="src" :class="{ on: source === s.value }" :aria-checked="source === s.value" @click="source = s.value">
            <Icon :name="s.icon" :size="18" />
            <span class="ln-ellipsis">{{ t('io.sources.' + s.value) }}</span>
          </button>
        </div>
      </section>

      <section v-if="source === 'notion_api'" class="fields">
        <div>
          <div class="label">{{ t('io.token') }}<InfoTip :text="t('io.tips.notion_api')" :width="320" /></div>
          <TextField v-model="token" type="password" mono autocomplete="off" placeholder="ntn_…" />
        </div>
        <div>
          <div class="label">{{ t('io.rootPage') }}<InfoTip :text="t('io.tips.rootPage')" :width="300" /></div>
          <TextField v-model="rootPage" :placeholder="t('io.rootPagePh')" />
        </div>
      </section>
      <section v-else>
        <div class="label">{{ t('io.file') }}<InfoTip :text="t('io.tips.' + source)" :width="320" /></div>
        <button type="button" class="drop" :class="{ over: dragging, has: !!file }" @click="choose"
          @dragover.prevent="dragging = true" @dragleave="dragging = false" @drop.prevent="onDrop">
          <Icon :name="file ? 'file' : 'upload'" :size="20" />
          <span v-if="file" class="fname ln-ellipsis">{{ file.name }}</span>
          <span v-else class="hint">{{ t('io.dropHint') }}</span>
          <span v-if="file" class="fsize">{{ formatSize(file.size) }}</span>
        </button>
      </section>

      <section>
        <div class="label">{{ t('io.target') }}</div>
        <PopoverRoot v-model:open="pickerOpen">
          <PopoverTrigger as-child>
            <button type="button" class="target">
              <template v-if="target"><PageIcon :icon="target.icon" :kind="target.kind" :size="16" /><span class="ln-ellipsis">{{ target.title || t('common.untitled') }}</span></template>
              <template v-else><Icon name="home" :size="16" /><span class="ln-ellipsis">{{ space?.name }}</span><span class="muted">{{ t('io.spaceRoot') }}</span></template>
              <Icon name="chevron-down" :size="14" class="chev" />
            </button>
          </PopoverTrigger>
          <PopoverPortal>
            <PopoverContent class="io-picker" align="start" :side-offset="4" :collision-padding="12">
              <input v-model="query" class="io-search" :placeholder="t('io.searchPages')" autofocus />
              <div class="io-list">
                <button v-if="!query" type="button" class="io-opt" :class="{ on: !targetId }" @click="pick('')">
                  <Icon name="home" :size="16" /><span class="ln-ellipsis">{{ space?.name }}</span><span class="muted">{{ t('io.spaceRoot') }}</span>
                </button>
                <button v-for="o in options" :key="o.page.id" type="button" class="io-opt" :class="{ on: targetId === o.page.id }"
                  :style="{ paddingLeft: 8 + o.depth * 14 + 'px' }" @click="pick(o.page.id)">
                  <PageIcon :icon="o.page.icon" :kind="o.page.kind" :size="16" /><span class="ln-ellipsis">{{ o.page.title || t('common.untitled') }}</span>
                </button>
                <div v-if="query && !options.length" class="io-empty">{{ t('io.noPages') }}</div>
              </div>
            </PopoverContent>
          </PopoverPortal>
        </PopoverRoot>
      </section>
    </div>

    <!-- 进行中 -->
    <div v-else-if="step === 'running'" class="running">
      <div class="phase">
        <span>{{ phaseText }}</span>
        <span v-if="job && job.progress.total > 0 && job.progress.phase === 'creating'" class="num">{{ job.progress.done }} / {{ job.progress.total }}</span>
      </div>
      <div class="bar" :class="{ indeterminate: pct < 0 }"><div class="fill" :style="{ width: (pct < 0 ? 30 : pct) + '%' }" /></div>
      <div class="current ln-ellipsis">{{ job?.progress.phase === 'creating' ? job.progress.current : '' }}</div>
      <p class="muted small">{{ t('io.background') }}</p>
    </div>

    <!-- 结果 -->
    <div v-else class="result">
      <div class="headline" :class="job?.status">
        <Icon :name="job?.status === 'done' ? 'check' : 'warning'" :size="18" />
        <span>{{ job?.status === 'done' ? t('io.phase.done') : t('io.phase.failed') }}</span>
      </div>
      <p v-if="report?.error" class="error">{{ report.error }}</p>
      <div v-if="counts.length" class="counts">
        <span v-for="c in counts" :key="c.key" class="count"><Icon :name="c.icon" :size="15" />{{ t('io.counts.' + c.key, { n: c.n }) }}</span>
      </div>
      <template v-if="report?.warnings?.length">
        <div class="label warn-head">{{ t('io.warnings', { n: report.warnings.length + (report.omitted_warnings || 0) }) }}</div>
        <ul class="warns">
          <li v-for="(w, i) in report.warnings" :key="i">
            <span>{{ warnText(w) }}</span>
            <span v-if="w.path || w.detail" class="where ln-ellipsis">{{ [w.path, w.code === 'file_too_large' ? '' : w.detail].filter(Boolean).join(' · ') }}</span>
          </li>
          <li v-if="report.omitted_warnings" class="muted">{{ t('io.omitted', { n: report.omitted_warnings }) }}</li>
        </ul>
      </template>
    </div>

    <template #footer>
      <template v-if="step === 'form'">
        <Btn @click="close">{{ t('common.cancel') }}</Btn>
        <Btn variant="primary" :loading="busy" :disabled="!ready" @click="start">{{ t('io.start') }}</Btn>
      </template>
      <template v-else-if="step === 'running'">
        <Btn v-if="!job && source !== 'notion_api'" @click="cancelUpload">{{ t('common.cancel') }}</Btn>
        <Btn @click="open = false">{{ t('io.hide') }}</Btn>
      </template>
      <template v-else>
        <Btn @click="reset">{{ t('io.again') }}</Btn>
        <Btn v-if="report?.root_ids?.length" variant="primary" @click="openResult">{{ t('io.openResult') }}</Btn>
        <Btn v-else variant="primary" @click="close">{{ t('common.done') }}</Btn>
      </template>
    </template>
  </Modal>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 18px; }
.label { font-size: 12.5px; font-weight: 500; color: var(--text-2); display: flex; align-items: center; gap: 4px; margin-bottom: 8px; }
.sources { display: grid; grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); gap: 6px; }
.src { display: flex; align-items: center; gap: 8px; height: 38px; padding: 0 10px; border-radius: var(--radius-m); border: 1px solid var(--border-strong);
  background: var(--bg); color: var(--text-2); text-align: left; min-width: 0; transition: border-color .12s, background .12s, color .12s; }
.src:hover { background: var(--bg-hover); color: var(--text); }
.src.on { border-color: var(--accent); box-shadow: 0 0 0 1px var(--accent) inset; color: var(--text); }
.src.on .ln-icon { color: var(--accent); }
.fields { display: flex; flex-direction: column; gap: 12px; }
.drop { width: 100%; display: flex; align-items: center; gap: 10px; min-height: 64px; padding: 12px 14px; border-radius: var(--radius-m);
  border: 1px dashed var(--border-strong); background: var(--bg-soft); color: var(--text-3); text-align: left; transition: border-color .12s, background .12s; }
.drop:hover, .drop.over { border-color: var(--accent); background: var(--accent-soft); }
.drop.has { color: var(--text-2); border-style: solid; }
.drop .hint { font-size: 13px; }
.drop .fname { flex: 1; color: var(--text); font-weight: 500; }
.drop .fsize { color: var(--text-3); font-size: 12.5px; flex: none; }
.target { width: 100%; display: flex; align-items: center; gap: 8px; height: 34px; padding: 0 10px; border-radius: var(--radius-m);
  border: 1px solid var(--border-strong); background: var(--bg); color: var(--text); text-align: left; }
.target:hover { background: var(--bg-hover); }
.target .chev { margin-left: auto; color: var(--text-3); }
.muted { color: var(--text-3); font-size: 12.5px; flex: none; }
.small { font-size: 12.5px; margin: 10px 0 0; }
.running { padding: 8px 0 4px; }
.phase { display: flex; justify-content: space-between; align-items: baseline; font-weight: 500; margin-bottom: 10px; }
.phase .num { color: var(--text-2); font-variant-numeric: tabular-nums; font-weight: 400; font-size: 13px; }
.bar { height: 6px; border-radius: 3px; background: var(--bg-sunken); overflow: hidden; }
.bar .fill { height: 100%; background: var(--accent); border-radius: 3px; transition: width .3s var(--ease); }
.bar.indeterminate .fill { animation: slide 1.2s var(--ease) infinite; }
@keyframes slide { from { transform: translateX(-100%); } to { transform: translateX(340%); } }
.current { color: var(--text-3); font-size: 12.5px; margin-top: 8px; min-height: 18px; }
.headline { display: flex; align-items: center; gap: 8px; font-weight: 600; font-size: 15px; }
.headline.done .ln-icon { color: var(--success); }
.headline.failed .ln-icon { color: var(--danger); }
.error { color: var(--danger); margin: 8px 0 0; font-size: 13px; line-height: 1.5; }
.counts { display: flex; flex-wrap: wrap; gap: 6px; margin-top: 14px; }
.count { display: inline-flex; align-items: center; gap: 6px; height: 28px; padding: 0 10px; border-radius: 14px; background: var(--bg-sunken); color: var(--text-2); font-size: 13px; }
.warn-head { margin: 18px 0 6px; }
.warns { list-style: none; margin: 0; padding: 0; max-height: 240px; overflow: auto; border: 1px solid var(--border); border-radius: var(--radius-m); }
.warns li { display: flex; flex-direction: column; gap: 2px; padding: 8px 12px; font-size: 13px; border-top: 1px solid var(--border); }
.warns li:first-child { border-top: 0; }
.warns .where { color: var(--text-3); font-size: 12px; font-family: var(--font-mono); }
</style>

<style>
.io-picker { width: max(300px, var(--reka-popover-trigger-width, 380px)); max-width: calc(100vw - 24px); background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 6px; z-index: 1200; }
.io-search { width: 100%; height: 30px; border: 0; border-bottom: 1px solid var(--border); background: transparent; outline: none; padding: 0 8px; margin-bottom: 4px; color: var(--text); }
.io-list { max-height: 280px; overflow: auto; }
.io-opt { width: 100%; display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 8px; border: 0; border-radius: 4px; background: none; color: var(--text); text-align: left; font-size: 13.5px; }
.io-opt:hover { background: var(--bg-hover); }
.io-opt.on { background: var(--bg-active); }
.io-opt .muted { color: var(--text-3); font-size: 12.5px; margin-left: auto; }
.io-empty { color: var(--text-3); font-size: 13px; padding: 10px 8px; }
</style>
