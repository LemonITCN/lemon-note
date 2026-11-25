import { Node, mergeAttributes, wrappingInputRule } from '@tiptap/core'
import { VueNodeViewRenderer } from '@tiptap/vue-3'
import CalloutView from '../nodes/CalloutView.vue'
import ToggleView from '../nodes/ToggleView.vue'
import SyncedBlockView from '../nodes/SyncedBlockView.vue'

declare module '@tiptap/core' {
  interface Commands<ReturnType> {
    lnBlocks: {
      insertCallout: () => ReturnType
      insertToggle: (level?: number) => ReturnType
      insertColumns: (n?: number) => ReturnType
    }
  }
}

export const Callout = Node.create({
  name: 'callout',
  group: 'block',
  content: 'block+',
  defining: true,
  addAttributes() {
    return { icon: { default: 'icon:info' } }
  },
  parseHTML() { return [{ tag: 'div[data-type="callout"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'callout' }), 0] },
  addNodeView() { return VueNodeViewRenderer(CalloutView) },
  addCommands() {
    return {
      insertCallout: () => ({ commands }) => commands.insertContent({ type: 'callout', attrs: { color: 'gray_background' }, content: [{ type: 'paragraph' }] }),
    }
  },
})

export const ToggleTitle = Node.create({
  name: 'toggleTitle',
  content: 'inline*',
  defining: true,
  parseHTML() { return [{ tag: 'div[data-type="toggleTitle"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'toggleTitle', class: 'ln-toggle-title' }), 0] },
})

export const ToggleBody = Node.create({
  name: 'toggleBody',
  content: 'block*',
  parseHTML() { return [{ tag: 'div[data-type="toggleBody"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'toggleBody', class: 'ln-toggle-body' }), 0] },
})

export const Toggle = Node.create({
  name: 'toggle',
  group: 'block',
  content: 'toggleTitle toggleBody',
  defining: true,
  addAttributes() {
    return { open: { default: true }, level: { default: 0 } }
  },
  parseHTML() { return [{ tag: 'div[data-type="toggle"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'toggle' }), 0] },
  addNodeView() { return VueNodeViewRenderer(ToggleView) },
  addCommands() {
    return {
      insertToggle: (level = 0) => ({ commands }) => commands.insertContent({ type: 'toggle', attrs: { open: true, level }, content: [{ type: 'toggleTitle' }, { type: 'toggleBody', content: [{ type: 'paragraph' }] }] }),
    }
  },
  addInputRules() {
    return [wrappingInputRule({ find: /^>>\s$/, type: this.type })]
  },
  addKeyboardShortcuts() {
    return {
      // 在折叠标题里回车：跳进内容区
      Enter: ({ editor }) => {
        const { $from } = editor.state.selection
        if ($from.parent.type.name !== 'toggleTitle') return false
        const toggle = $from.node($from.depth - 1)
        const togglePos = $from.before($from.depth - 1)
        if (!toggle.attrs.open) editor.view.dispatch(editor.state.tr.setNodeMarkup(togglePos, undefined, { ...toggle.attrs, open: true }))
        const bodyStart = togglePos + 1 + toggle.child(0).nodeSize + 1
        return editor.chain().setTextSelection(bodyStart + 1).run()
      },
    }
  },
})

export const Column = Node.create({
  name: 'column',
  content: 'block+',
  isolating: true,
  addAttributes() { return { width: { default: null } } },
  parseHTML() { return [{ tag: 'div[data-type="column"]' }] },
  renderHTML({ HTMLAttributes, node }) {
    const w = node.attrs.width
    return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'column', class: 'ln-column', style: w ? `flex: ${w} 1 0` : 'flex: 1 1 0' }), 0]
  },
})

export const Columns = Node.create({
  name: 'columns',
  group: 'block',
  content: 'column{2,}',
  defining: true,
  parseHTML() { return [{ tag: 'div[data-type="columns"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'columns', class: 'ln-columns' }), 0] },
  addCommands() {
    return {
      insertColumns: (n = 2) => ({ commands }) => commands.insertContent({ type: 'columns', content: Array.from({ length: n }, () => ({ type: 'column', content: [{ type: 'paragraph' }] })) }),
    }
  },
})

export const SyncedBlock = Node.create({
  name: 'syncedBlock',
  group: 'block',
  content: 'block*',
  addAttributes() { return { sourceId: { default: null } } },
  parseHTML() { return [{ tag: 'div[data-type="syncedBlock"]' }] },
  renderHTML({ HTMLAttributes }) { return ['div', mergeAttributes(HTMLAttributes, { 'data-type': 'syncedBlock' }), 0] },
  addNodeView() { return VueNodeViewRenderer(SyncedBlockView) },
})
