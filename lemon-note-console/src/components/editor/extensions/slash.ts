import { Extension, type Editor, type Range } from '@tiptap/core'
import Suggestion from '@tiptap/suggestion'
import { PluginKey } from '@tiptap/pm/state'
import { reactive } from 'vue'
import { i18n } from '@/i18n'

export interface SlashItem { key: string; label: string; icon: string; group: string; keywords: string; run: (editor: Editor, range: Range) => void }

export const slashState = reactive({ open: false, items: [] as SlashItem[], index: 0, rect: null as DOMRect | null, command: null as null | ((it: SlashItem) => void) })

const t = (k: string) => i18n.global.t(k)

function items(): SlashItem[] {
  const g = { b: t('editor.basic'), m: t('editor.media'), d: t('editor.diagrams'), a: t('editor.advanced') }
  const del = (e: Editor, r: Range) => e.chain().focus().deleteRange(r)
  return [
    { key: 'text', label: t('editor.text'), icon: 'text', group: g.b, keywords: 'text paragraph zhengwen wenben', run: (e, r) => del(e, r).setParagraph().run() },
    { key: 'h1', label: t('editor.h1'), icon: 'h1', group: g.b, keywords: 'h1 heading biaoti', run: (e, r) => del(e, r).setHeading({ level: 1 }).run() },
    { key: 'h2', label: t('editor.h2'), icon: 'h2', group: g.b, keywords: 'h2 heading biaoti', run: (e, r) => del(e, r).setHeading({ level: 2 }).run() },
    { key: 'h3', label: t('editor.h3'), icon: 'h3', group: g.b, keywords: 'h3 heading biaoti', run: (e, r) => del(e, r).setHeading({ level: 3 }).run() },
    { key: 'bullet', label: t('editor.bullet'), icon: 'list-bullet', group: g.b, keywords: 'bullet list ul liebiao', run: (e, r) => del(e, r).toggleBulletList().run() },
    { key: 'numbered', label: t('editor.numbered'), icon: 'list-number', group: g.b, keywords: 'number ordered ol youxu', run: (e, r) => del(e, r).toggleOrderedList().run() },
    { key: 'todo', label: t('editor.todo'), icon: 'list-check', group: g.b, keywords: 'todo task check daiban', run: (e, r) => del(e, r).toggleTaskList().run() },
    { key: 'toggle', label: t('editor.toggle'), icon: 'toggle', group: g.b, keywords: 'toggle details fold zhedie', run: (e, r) => del(e, r).insertToggle().run() },
    { key: 'quote', label: t('editor.quote'), icon: 'quote', group: g.b, keywords: 'quote blockquote yinyong', run: (e, r) => del(e, r).toggleBlockquote().run() },
    { key: 'callout', label: t('editor.callout'), icon: 'callout', group: g.b, keywords: 'callout note tip biaozhu', run: (e, r) => del(e, r).insertCallout().run() },
    { key: 'divider', label: t('editor.divider'), icon: 'divider', group: g.b, keywords: 'divider hr line fenge', run: (e, r) => del(e, r).setHorizontalRule().run() },
    { key: 'page', label: t('editor.page'), icon: 'page', group: g.b, keywords: 'page subpage yemian', run: (e, r) => { del(e, r).run(); e.emit('lnCreateSubpage' as any, {}) } },
    { key: 'link', label: t('editor.linkToPage'), icon: 'arrow-up-right', group: g.b, keywords: 'link page lianjie', run: (e, r) => { del(e, r).insertContent('@').run() } },
    { key: 'table', label: t('editor.table'), icon: 'table', group: g.a, keywords: 'table biaoge', run: (e, r) => del(e, r).insertTable({ rows: 3, cols: 3, withHeaderRow: true }).run() },
    { key: 'columns2', label: t('editor.columns') + ' 2', icon: 'columns', group: g.a, keywords: 'columns fenlan', run: (e, r) => del(e, r).insertColumns(2).run() },
    { key: 'columns3', label: t('editor.columns') + ' 3', icon: 'columns', group: g.a, keywords: 'columns fenlan', run: (e, r) => del(e, r).insertColumns(3).run() },
    { key: 'code', label: t('editor.code'), icon: 'code', group: g.a, keywords: 'code daima', run: (e, r) => del(e, r).toggleCodeBlock().run() },
    { key: 'math', label: t('editor.math'), icon: 'math', group: g.a, keywords: 'math equation latex gongshi', run: (e, r) => del(e, r).insertContent({ type: 'mathBlock', attrs: { latex: '' } }).run() },
    { key: 'toc', label: t('editor.toc'), icon: 'toc', group: g.a, keywords: 'toc mulu', run: (e, r) => del(e, r).insertContent({ type: 'tableOfContents' }).run() },
    { key: 'breadcrumb', label: t('editor.breadcrumb'), icon: 'arrow-right', group: g.a, keywords: 'breadcrumb', run: (e, r) => del(e, r).insertContent({ type: 'breadcrumb' }).run() },
    { key: 'database', label: t('editor.database'), icon: 'database', group: g.a, keywords: 'database table shujuku', run: (e, r) => { del(e, r).run(); e.emit('lnCreateDatabase' as any, {}) } },
    { key: 'image', label: t('editor.image'), icon: 'image', group: g.m, keywords: 'image picture tupian', run: (e, r) => del(e, r).insertContent({ type: 'image', attrs: { src: '' } }).run() },
    { key: 'file', label: t('editor.file'), icon: 'paperclip', group: g.m, keywords: 'file attachment wenjian', run: (e, r) => del(e, r).insertContent({ type: 'fileBlock' }).run() },
    { key: 'video', label: t('editor.video'), icon: 'video', group: g.m, keywords: 'video shipin', run: (e, r) => del(e, r).insertContent({ type: 'video' }).run() },
    { key: 'audio', label: t('editor.audio'), icon: 'audio', group: g.m, keywords: 'audio yinpin', run: (e, r) => del(e, r).insertContent({ type: 'audio' }).run() },
    { key: 'bookmark', label: t('editor.bookmark'), icon: 'bookmark', group: g.m, keywords: 'bookmark url shuqian', run: (e, r) => del(e, r).insertContent({ type: 'bookmark' }).run() },
    { key: 'embed', label: t('editor.embed'), icon: 'globe', group: g.m, keywords: 'embed iframe qianru', run: (e, r) => del(e, r).insertContent({ type: 'embed' }).run() },
    { key: 'mermaid', label: t('editor.mermaid'), icon: 'flowchart', group: g.d, keywords: 'mermaid flowchart sequence gantt liucheng', run: (e, r) => del(e, r).insertContent({ type: 'mermaid', attrs: { code: 'flowchart LR\n  A[开始] --> B{判断}\n  B -->|是| C[处理]\n  B -->|否| D[结束]' } }).run() },
    { key: 'mindmap', label: t('editor.mindmap'), icon: 'mindmap', group: g.d, keywords: 'mindmap siwei daotu', run: (e, r) => del(e, r).insertContent({ type: 'mindmap' }).run() },
    { key: 'whiteboard', label: t('editor.whiteboard'), icon: 'whiteboard', group: g.d, keywords: 'whiteboard excalidraw draw baiban huatu', run: (e, r) => del(e, r).insertContent({ type: 'whiteboard' }).run() },
    { key: 'chart', label: t('editor.chart'), icon: 'chart', group: g.d, keywords: 'chart echarts tubiao', run: (e, r) => del(e, r).insertContent({ type: 'chart' }).run() },
    { key: 'graphviz', label: t('editor.graphviz'), icon: 'graph', group: g.d, keywords: 'graphviz dot graph', run: (e, r) => del(e, r).insertContent({ type: 'graphviz', attrs: { code: 'digraph G {\n  rankdir=LR\n  A -> B -> C\n}', engine: 'dot' } }).run() },
  ]
}

export const SlashCommand = Extension.create({
  name: 'slashCommand',
  addProseMirrorPlugins() {
    return [Suggestion({
      editor: this.editor,
      pluginKey: new PluginKey('lnSlash'),
      char: '/',
      allowSpaces: false,
      startOfLine: false,
      allow: ({ state, range }) => {
        const $from = state.doc.resolve(range.from)
        return $from.parent.type.name === 'paragraph' || $from.parent.type.name === 'heading'
      },
      items: ({ query }) => {
        const q = query.toLowerCase()
        return items().filter((it) => !q || it.label.toLowerCase().includes(q) || it.keywords.includes(q) || it.key.includes(q)).slice(0, 40)
      },
      command: ({ editor, range, props }) => (props as SlashItem).run(editor, range),
      render: () => ({
        onStart: (p) => { Object.assign(slashState, { open: true, items: p.items, index: 0, rect: p.clientRect?.() || null, command: (it: SlashItem) => p.command(it) }) },
        onUpdate: (p) => { Object.assign(slashState, { items: p.items, index: 0, rect: p.clientRect?.() || null, command: (it: SlashItem) => p.command(it) }) },
        onKeyDown: ({ event }) => {
          if (!slashState.open) return false
          if (event.key === 'ArrowDown') { slashState.index = (slashState.index + 1) % Math.max(1, slashState.items.length); return true }
          if (event.key === 'ArrowUp') { slashState.index = (slashState.index - 1 + slashState.items.length) % Math.max(1, slashState.items.length); return true }
          if (event.key === 'Enter') { const it = slashState.items[slashState.index]; if (it) slashState.command?.(it); return true }
          if (event.key === 'Escape') { slashState.open = false; return true }
          return false
        },
        onExit: () => { slashState.open = false },
      }),
    })]
  },
})
