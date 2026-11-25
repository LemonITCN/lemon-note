import Mention from '@tiptap/extension-mention'
import { VueNodeViewRenderer } from '@tiptap/vue-3'
import { PluginKey } from '@tiptap/pm/state'
import { reactive } from 'vue'
import MentionView from '../nodes/MentionView.vue'
import { useWorkspace } from '@/stores/workspace'
import { api } from '@/api/client'
import { i18n } from '@/i18n'

export interface MentionItem { kind: 'page' | 'user' | 'date' | 'new'; id: string; label: string; icon?: string; date?: string; sub?: string }

export const mentionState = reactive({ open: false, items: [] as MentionItem[], index: 0, rect: null as DOMRect | null, command: null as null | ((it: MentionItem) => void) })

let users: { id: string; display_name: string; username: string }[] | null = null
async function loadUsers() {
  if (!users) {
    try { users = (await api.get('/api/users')).users } catch { users = [] }
  }
  return users!
}

function fmt(d: Date) {
  const p = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}`
}

export const LnMention = Mention.extend({
  addAttributes() {
    return {
      kind: { default: 'page', parseHTML: (el) => el.getAttribute('data-kind'), renderHTML: (a) => ({ 'data-kind': a.kind }) },
      id: { default: null, parseHTML: (el) => el.getAttribute('data-id'), renderHTML: (a) => (a.id ? { 'data-id': a.id } : {}) },
      date: { default: null, parseHTML: (el) => el.getAttribute('data-date'), renderHTML: (a) => (a.date ? { 'data-date': a.date } : {}) },
      endDate: { default: null, parseHTML: (el) => el.getAttribute('data-end'), renderHTML: (a) => (a.endDate ? { 'data-end': a.endDate } : {}) },
    }
  },
  addNodeView() { return VueNodeViewRenderer(MentionView as any) },
}).configure({
  suggestion: {
    char: '@',
    pluginKey: new PluginKey('lnMention'),
    items: async ({ query }) => {
      const ws = useWorkspace()
      const q = query.toLowerCase()
      const t = i18n.global.t
      const out: MentionItem[] = []
      const today = new Date()
      const dates: [string, Date][] = [[i18n.global.locale.value === 'en' ? 'Today' : '今天', today], [i18n.global.locale.value === 'en' ? 'Tomorrow' : '明天', new Date(today.getTime() + 86400000)]]
      for (const [label, d] of dates) if (!q || label.toLowerCase().includes(q)) out.push({ kind: 'date', id: '', label, date: fmt(d), sub: fmt(d) })
      if (/^\d{4}-\d{2}-\d{2}$/.test(query)) out.push({ kind: 'date', id: '', label: query, date: query })
      const pages = [...ws.pages.values()].filter((p) => !p.trashed_at && (!q || p.title.toLowerCase().includes(q))).sort((a, b) => b.updated_at - a.updated_at).slice(0, 8)
      for (const p of pages) out.push({ kind: 'page', id: p.id, label: p.title || t('common.untitled'), icon: p.icon })
      for (const u of (await loadUsers()).filter((u) => !q || u.display_name.toLowerCase().includes(q) || u.username.includes(q)).slice(0, 5)) out.push({ kind: 'user', id: u.id, label: u.display_name, sub: '@' + u.username })
      return out
    },
    command: ({ editor, range, props }) => {
      const it = props as unknown as MentionItem
      editor.chain().focus().insertContentAt(range, [{ type: 'mention', attrs: { kind: it.kind, id: it.id || null, date: it.date || null, label: it.label } }, { type: 'text', text: ' ' }]).run()
    },
    render: () => ({
      onStart: (p) => { Object.assign(mentionState, { open: true, items: p.items, index: 0, rect: p.clientRect?.() || null, command: (it: MentionItem) => p.command(it as any) }) },
      onUpdate: (p) => { Object.assign(mentionState, { items: p.items, index: 0, rect: p.clientRect?.() || null, command: (it: MentionItem) => p.command(it as any) }) },
      onKeyDown: ({ event }) => {
        if (!mentionState.open) return false
        const n = Math.max(1, mentionState.items.length)
        if (event.key === 'ArrowDown') { mentionState.index = (mentionState.index + 1) % n; return true }
        if (event.key === 'ArrowUp') { mentionState.index = (mentionState.index - 1 + n) % n; return true }
        if (event.key === 'Enter') { const it = mentionState.items[mentionState.index]; if (it) mentionState.command?.(it); return true }
        if (event.key === 'Escape') { mentionState.open = false; return true }
        return false
      },
      onExit: () => { mentionState.open = false },
    }),
  },
})
