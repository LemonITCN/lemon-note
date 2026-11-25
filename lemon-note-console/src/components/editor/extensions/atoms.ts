import { Node, mergeAttributes, InputRule } from '@tiptap/core'
import { VueNodeViewRenderer } from '@tiptap/vue-3'
import type { Component } from 'vue'
import MathBlockView from '../nodes/MathBlockView.vue'
import MathInlineView from '../nodes/MathInlineView.vue'
import DiagramView from '../nodes/DiagramView.vue'
import MindmapView from '../nodes/MindmapView.vue'
import WhiteboardView from '../nodes/WhiteboardView.vue'
import ChartView from '../nodes/ChartView.vue'
import PageRefView from '../nodes/PageRefView.vue'
import ImageView from '../nodes/ImageView.vue'
import FileView from '../nodes/FileView.vue'
import MediaView from '../nodes/MediaView.vue'
import BookmarkView from '../nodes/BookmarkView.vue'
import EmbedView from '../nodes/EmbedView.vue'
import TocView from '../nodes/TocView.vue'
import BreadcrumbView from '../nodes/BreadcrumbView.vue'


function encode(v: unknown) {
  return v === null || v === undefined ? null : typeof v === 'string' ? v : JSON.stringify(v)
}
function decode(s: string | null, def: unknown) {
  if (s === null) return def
  if (typeof def === 'string' || def === null) {
    if (def === null && s.startsWith('{')) { try { return JSON.parse(s) } catch { return s } }
    return s
  }
  try { return JSON.parse(s) } catch { return def }
}

/** 生成一个原子块节点：属性序列化到 data-* 上，便于复制粘贴时保真。 */
function atomBlock(name: string, attrs: Record<string, unknown>, view: Component, inline = false) {
  return Node.create({
    name,
    group: inline ? 'inline' : 'block',
    inline,
    atom: true,
    draggable: !inline,
    selectable: true,
    addAttributes() {
      const out: Record<string, any> = {}
      for (const [k, def] of Object.entries(attrs)) {
        out[k] = {
          default: def,
          parseHTML: (el: HTMLElement) => decode(el.getAttribute('data-' + k.toLowerCase()), def),
          renderHTML: (a: Record<string, unknown>) => (a[k] === null || a[k] === undefined ? {} : { ['data-' + k.toLowerCase()]: encode(a[k]) }),
        }
      }
      return out
    },
    parseHTML() { return [{ tag: `${inline ? 'span' : 'div'}[data-type="${name}"]` }] },
    renderHTML({ HTMLAttributes }) { return [inline ? 'span' : 'div', mergeAttributes(HTMLAttributes, { 'data-type': name })] },
    addNodeView() { return VueNodeViewRenderer(view as any) },
  })
}

export const MathBlock = atomBlock('mathBlock', { latex: '' }, MathBlockView)
export const MathInline = atomBlock('mathInline', { latex: '' }, MathInlineView, true).extend({
  addInputRules() {
    return [new InputRule({
      find: /(?:^|\s)\$([^$\s][^$]*?[^$\s]|[^$\s])\$$/,
      handler: ({ state, range, match }) => {
        const latex = match[1]
        const start = range.from + (match[0].startsWith('$') ? 0 : 1)
        state.tr.replaceWith(start, range.to, this.type.create({ latex }))
      },
    })]
  },
})
export const Mermaid = atomBlock('mermaid', { code: '' }, DiagramView)
export const Graphviz = atomBlock('graphviz', { code: '', engine: 'dot' }, DiagramView)
export const Mindmap = atomBlock('mindmap', { data: null }, MindmapView)
export const Whiteboard = atomBlock('whiteboard', { data: null, title: '' }, WhiteboardView)
export const Chart = atomBlock('chart', { data: null }, ChartView)
export const ChildPage = atomBlock('childPage', { pageId: '' }, PageRefView)
export const ChildDatabase = atomBlock('childDatabase', { pageId: '', viewId: '' }, PageRefView)
export const LinkToPage = atomBlock('linkToPage', { pageId: '' }, PageRefView)
export const Image = atomBlock('image', { src: '', alt: '', title: '', width: null, align: 'center', uploadToken: null }, ImageView)
export const FileBlock = atomBlock('fileBlock', { src: '', name: '', size: 0, mime: '', uploadToken: null }, FileView)
export const Video = atomBlock('video', { src: '', title: '', uploadToken: null }, MediaView)
export const Audio = atomBlock('audio', { src: '', title: '', uploadToken: null }, MediaView)
export const Bookmark = atomBlock('bookmark', { url: '', title: '', description: '', image: '' }, BookmarkView)
export const Embed = atomBlock('embed', { url: '', height: 400 }, EmbedView)
export const TableOfContents = atomBlock('tableOfContents', {}, TocView)
export const Breadcrumb = atomBlock('breadcrumb', {}, BreadcrumbView)
