// 智能体：会话列表、当前会话、运行事件流、引用草稿。
import { defineStore } from 'pinia'
import { ref, computed, reactive, watch } from 'vue'
import { api } from '@/api/client'
import type { ChatRef } from '@/lib/chatRefs'
import { chatInbox } from '@/lib/chatRefs'
import { useUI } from './ui'

export interface Part {
  type: 'text' | 'image' | 'reasoning' | 'tool_call' | 'tool_result'
  text?: string
  call_id?: string
  name?: string
  args?: any
  is_error?: boolean
  refs?: ChatRef[]
  title?: string
  data?: any
  status?: string
  duration?: number
  media_type?: string
}
export interface Message { id: string; seq: number; role: 'user' | 'assistant' | 'tool'; parts: Part[]; created_at: number; meta?: any }
export interface ConvSummary { id: string; title: string; updated_at: number; settings: ConvSettings; running?: boolean }
export interface ConvSettings { model: string; effort: string; mode: string }
export interface ModelInfo { id: string; name: string; description?: string; context: number; vision: boolean; efforts?: string[]; default_effort?: string; featured?: boolean }
export interface Todo { content: string; status: 'pending' | 'in_progress' | 'completed' }
export interface Interaction { id: string; kind: 'approval' | 'question'; call_id?: string; name?: string; title?: string; args?: any; destructive?: boolean; questions?: any[] }
export interface LiveTool { call_id: string; name: string; title: string; args?: any; status: 'preparing' | 'running' | 'done' | 'error' | 'denied' | 'interrupted'; text?: string; data?: any; duration?: number }

export const useAgent = defineStore('agent', () => {
  const conversations = ref<ConvSummary[]>([])
  const currentId = ref<string>(localStorage.getItem('ln.agent.conv') || '')
  const messages = ref<Message[]>([])
  const title = ref('')
  const settings = reactive<ConvSettings>({ model: '', effort: 'high', mode: 'ask_destructive' })
  const models = ref<ModelInfo[]>([])
  const ready = ref(true)
  const providerError = ref('')
  const defaultModel = ref('')
  const draftRefs = ref<ChatRef[]>([])
  const runId = ref('')
  const running = ref(false)
  const live = reactive({ text: '', reasoning: '', active: false, tools: [] as LiveTool[], notice: '', retry: '', compacting: false, sub: '' })
  const toolStatus = reactive(new Map<string, LiveTool>())
  const interactions = ref<Interaction[]>([])
  const todos = ref<Todo[]>([])
  const context = ref(0)
  const budget = ref(160000)
  const lastCheckpoint = ref<{ id: string; pages: number } | null>(null)
  const checkpoints = ref<any[]>([])
  const error = ref('')
  let es: EventSource | null = null
  let lastSeq = 0

  const contextPct = computed(() => Math.min(100, Math.round((context.value / Math.max(1, budget.value)) * 100)))
  const currentModel = computed(() => models.value.find((m) => m.id === (settings.model || defaultModel.value)))

  watch(currentId, (v) => { try { localStorage.setItem('ln.agent.conv', v) } catch {} })

  // 编辑器、页面树的「添加到聊天」
  watch(() => chatInbox.pending.length, () => {
    if (!chatInbox.pending.length) return
    for (const r of chatInbox.pending.splice(0)) addRef(r)
    useUI().toggleAgent(true)
  })

  function addRef(r: ChatRef) {
    const key = (x: ChatRef) => x.kind + ':' + x.id + ':' + (x.text || '').slice(0, 40)
    if (!draftRefs.value.some((x) => key(x) === key(r))) draftRefs.value.push(r)
  }

  async function loadModels() {
    try {
      const r = await api.get('/api/agent/models')
      ready.value = r.ready
      providerError.value = r.error || ''
      models.value = r.models || []
      defaultModel.value = r.default || ''
    } catch (e: any) { ready.value = false; providerError.value = e.message }
  }

  async function loadList() {
    conversations.value = (await api.get('/api/agent/conversations')).conversations
  }

  function resetLive() {
    Object.assign(live, { text: '', reasoning: '', active: false, tools: [], notice: '', retry: '', compacting: false, sub: '' })
  }

  async function open(id: string) {
    closeStream()
    resetLive()
    interactions.value = []
    error.value = ''
    lastCheckpoint.value = null
    if (!id) { currentId.value = ''; messages.value = []; title.value = ''; todos.value = []; context.value = 0; return }
    const r = await api.get(`/api/agent/conversations/${id}`)
    currentId.value = id
    messages.value = r.messages
    title.value = r.conversation.title
    Object.assign(settings, { model: r.conversation.settings.model || '', effort: r.conversation.settings.effort || 'high', mode: r.conversation.settings.mode || 'ask_destructive' })
    todos.value = r.todos || []
    context.value = r.context || 0
    checkpoints.value = r.checkpoints || []
    const last = (r.checkpoints || []).filter((c: any) => !c.reverted_at).at(-1)
    if (last && !r.active_run) lastCheckpoint.value = { id: last.id, pages: Object.keys(JSON.parse(last.changes || '{}').pages || {}).length }
    if (r.active_run) {
      runId.value = r.active_run.id
      running.value = true
      interactions.value = r.active_run.pending || []
      connect(r.active_run.id, r.active_run.resume_from)
    } else {
      running.value = false
    }
  }

  async function refreshMessages() {
    if (!currentId.value) return
    const r = await api.get(`/api/agent/conversations/${currentId.value}`)
    messages.value = r.messages
    title.value = r.conversation.title
  }

  async function newConversation() {
    closeStream()
    currentId.value = ''
    messages.value = []
    title.value = ''
    todos.value = []
    context.value = 0
    lastCheckpoint.value = null
    resetLive()
  }

  async function send(text: string, currentPage: string, images: string[] = []) {
    error.value = ''
    if (!currentId.value) {
      const r = await api.post('/api/agent/conversations', { settings: { ...settings } })
      currentId.value = r.conversation.id
    }
    const refs = draftRefs.value.slice()
    draftRefs.value = []
    const r = await api.post(`/api/agent/conversations/${currentId.value}/messages`, {
      text, refs, images: images.map((id) => ({ file_id: id })), model: settings.model, effort: settings.effort, mode: settings.mode, current_page: currentPage,
    })
    if (!running.value || r.run_id !== runId.value) {
      messages.value.push(r.message)
      runId.value = r.run_id
      running.value = true
      lastCheckpoint.value = null
      resetLive()
      connect(r.run_id, 0, true)
    }
    loadList().catch(() => {})
  }

  function closeStream() {
    es?.close()
    es = null
  }

  function connect(id: string, after = 0, skipUserEcho = false) {
    closeStream()
    lastSeq = after
    const src = new EventSource(`/api/agent/runs/${id}/events?after=${after}`)
    es = src
    src.onmessage = (m) => {
      let ev: any
      try { ev = JSON.parse(m.data) } catch { return }
      if (ev.seq <= lastSeq) return
      lastSeq = ev.seq
      handle(ev.type, ev.data || {}, skipUserEcho)
    }
    src.onerror = () => {
      if (!running.value) closeStream()
    }
  }

  function handle(type: string, d: any, skipUserEcho: boolean) {
    switch (type) {
      case 'user_message':
        if (!messages.value.some((m) => m.id === d.id) && !(skipUserEcho && messages.value.at(-1)?.id === d.id)) messages.value.push(d)
        break
      case 'assistant_start':
        live.active = true
        live.text = ''
        live.reasoning = ''
        live.tools = []
        live.retry = ''
        break
      case 'text': live.text += d.text; break
      case 'reasoning': live.reasoning += d.text; break
      case 'step_reset': live.text = ''; live.reasoning = ''; live.tools = []; break
      case 'retry': live.retry = `${d.error}，${Math.round(d.wait)} 秒后重试（第 ${d.attempt} 次）`; break
      case 'tool_start':
        live.tools.push({ call_id: d.call_id, name: d.name, title: d.title, status: 'preparing' })
        break
      case 'tool_call': {
        const t = live.tools.find((x) => x.call_id === d.call_id)
        if (t) { t.args = d.args; t.status = 'running' }
        else live.tools.push({ call_id: d.call_id, name: d.name, title: d.title, args: d.args, status: 'running' })
        toolStatus.set(d.call_id, { call_id: d.call_id, name: d.name, title: d.title, args: d.args, status: 'running' })
        break
      }
      case 'tool_result': {
        const st = { call_id: d.call_id, name: d.name, title: '', status: d.status, text: d.text, data: d.data, duration: d.duration } as LiveTool
        toolStatus.set(d.call_id, { ...(toolStatus.get(d.call_id) || st), ...st })
        break
      }
      case 'message_saved':
        refreshMessages().then(() => { if (d.role === 'assistant') { live.active = false; live.text = ''; live.reasoning = ''; live.tools = [] } })
        break
      case 'approval':
      case 'question':
        if (!interactions.value.some((x) => x.id === d.id)) interactions.value.push({ ...d, kind: type })
        break
      case 'interaction_done':
        interactions.value = interactions.value.filter((x) => x.id !== d.id)
        break
      case 'todos': todos.value = d || []; break
      case 'usage': context.value = d.context; budget.value = d.budget || budget.value; break
      case 'notice': live.notice = d.text; break
      case 'compaction_start': live.compacting = true; break
      case 'compaction_done': live.compacting = false; break
      case 'sub_start': live.sub = d.task; break
      case 'sub_progress': live.sub = `${d.task} · ${d.title || d.tool}`; break
      case 'sub_done': live.sub = ''; break
      case 'run_done':
        running.value = false
        interactions.value = []
        live.active = false
        if (d.status === 'error') error.value = d.error || '执行失败'
        if (d.checkpoint_id) lastCheckpoint.value = { id: d.checkpoint_id, pages: d.changed_pages }
        closeStream()
        refreshMessages().then(() => resetLive())
        loadList().catch(() => {})
        break
    }
  }

  async function stop() {
    if (runId.value) await api.post(`/api/agent/runs/${runId.value}/stop`)
  }

  async function respond(id: string, body: { approve?: boolean; always?: boolean; answer?: string }) {
    await api.post(`/api/agent/runs/${runId.value}/respond`, { id, ...body })
    interactions.value = interactions.value.filter((x) => x.id !== id)
  }

  async function revert() {
    if (!lastCheckpoint.value) return 0
    const r = await api.post(`/api/agent/checkpoints/${lastCheckpoint.value.id}/revert`)
    lastCheckpoint.value = null
    return r.pages as number
  }

  async function saveSettings() {
    if (currentId.value) await api.patch(`/api/agent/conversations/${currentId.value}`, { settings: { ...settings } })
  }

  async function rename(id: string, t: string) {
    await api.patch(`/api/agent/conversations/${id}`, { title: t })
    if (id === currentId.value) title.value = t
    await loadList()
  }

  async function remove(id: string) {
    await api.del(`/api/agent/conversations/${id}`)
    if (id === currentId.value) await newConversation()
    await loadList()
  }

  return { conversations, currentId, messages, title, settings, models, ready, providerError, defaultModel, draftRefs, runId, running, live, toolStatus,
    interactions, todos, context, budget, contextPct, currentModel, lastCheckpoint, error, checkpoints,
    addRef, loadModels, loadList, open, newConversation, send, stop, respond, revert, saveSettings, rename, remove, refreshMessages }
})
