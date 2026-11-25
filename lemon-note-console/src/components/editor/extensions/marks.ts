import { Mark, Extension, mergeAttributes } from '@tiptap/core'

export const COLORS = ['gray', 'brown', 'orange', 'yellow', 'green', 'blue', 'purple', 'pink', 'red'] as const

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    textColor: { setTextColor: (color: string) => ReturnType; unsetTextColor: () => ReturnType }
    lnHighlight: { setHighlight: (color?: string) => ReturnType; unsetHighlight: () => ReturnType; toggleHighlight: (color?: string) => ReturnType }
    blockColor: { setBlockColor: (color: string | null) => ReturnType }
  }
}

/** 文字颜色（命名颜色，跟随主题）。 */
export const TextColor = Mark.create({
  name: 'textColor',
  addAttributes() {
    return { color: { default: 'gray', parseHTML: (el) => el.getAttribute('data-color'), renderHTML: (a) => ({ 'data-color': a.color, class: 'c-' + a.color }) } }
  },
  parseHTML() { return [{ tag: 'span[data-color]' }] },
  renderHTML({ HTMLAttributes }) { return ['span', mergeAttributes(HTMLAttributes), 0] },
  addCommands() {
    return {
      setTextColor: (color) => ({ commands }) => commands.setMark(this.name, { color }),
      unsetTextColor: () => ({ commands }) => commands.unsetMark(this.name),
    }
  },
})

/** 背景高亮（命名颜色）。 */
export const Highlight = Mark.create({
  name: 'highlight',
  addAttributes() {
    return { color: { default: 'yellow', parseHTML: (el) => el.getAttribute('data-bg') || 'yellow', renderHTML: (a) => ({ 'data-bg': a.color, class: 'cb-' + a.color }) } }
  },
  parseHTML() { return [{ tag: 'mark' }] },
  renderHTML({ HTMLAttributes }) { return ['mark', mergeAttributes(HTMLAttributes), 0] },
  addCommands() {
    return {
      setHighlight: (color = 'yellow') => ({ commands }) => commands.setMark(this.name, { color }),
      unsetHighlight: () => ({ commands }) => commands.unsetMark(this.name),
      toggleHighlight: (color = 'yellow') => ({ commands }) => commands.toggleMark(this.name, { color }),
    }
  },
  addKeyboardShortcuts() { return { 'Mod-Shift-h': () => this.editor.commands.toggleHighlight() } },
})

/** 评论标记（锚定评论线程，Markdown 里不显示）。 */
export const CommentMark = Mark.create({
  name: 'comment',
  inclusive: false,
  addAttributes() {
    return { threadId: { default: null, parseHTML: (el) => el.getAttribute('data-thread'), renderHTML: (a) => ({ 'data-thread': a.threadId }) } }
  },
  parseHTML() { return [{ tag: 'span[data-thread]' }] },
  renderHTML({ HTMLAttributes }) { return ['span', mergeAttributes(HTMLAttributes, { class: 'ln-comment' }), 0] },
})

/** 块颜色：文字类块可整体着色（color 或 color_background）。 */
export const BlockColor = Extension.create({
  name: 'blockColor',
  addGlobalAttributes() {
    return [{
      types: ['paragraph', 'heading', 'bulletList', 'orderedList', 'taskList', 'blockquote', 'toggle', 'callout'],
      attributes: {
        color: {
          default: null,
          parseHTML: (el) => el.getAttribute('data-block-color'),
          renderHTML: (a) => {
            if (!a.color) return {}
            const c = String(a.color)
            return { 'data-block-color': c, class: c.endsWith('_background') ? 'cb-' + c.replace('_background', '') + ' has-bg' : 'c-' + c }
          },
        },
      },
    }]
  },
  addCommands() {
    return {
      setBlockColor: (color) => ({ tr, state, dispatch }) => {
        const { $from } = state.selection
        for (let d = $from.depth; d > 0; d--) {
          const node = $from.node(d)
          if (node.type.spec.attrs && 'color' in node.type.spec.attrs) {
            if (dispatch) tr.setNodeMarkup($from.before(d), undefined, { ...node.attrs, color })
            return true
          }
        }
        return false
      },
    }
  },
})
