import type { AnyExtension } from '@tiptap/core'
import StarterKit from '@tiptap/starter-kit'
import { TaskList, TaskItem } from '@tiptap/extension-list'
import { Table, TableRow, TableHeader, TableCell } from '@tiptap/extension-table'
import TextAlign from '@tiptap/extension-text-align'
import Subscript from '@tiptap/extension-subscript'
import Superscript from '@tiptap/extension-superscript'
import { Placeholder } from '@tiptap/extensions'
import UniqueID from '@tiptap/extension-unique-id'
import CodeBlockLowlight from '@tiptap/extension-code-block-lowlight'
import { common, createLowlight } from 'lowlight'
import { TextColor, Highlight, CommentMark, BlockColor } from './marks'
import { Callout, Toggle, ToggleTitle, ToggleBody, Columns, Column, SyncedBlock } from './structure'
import * as A from './atoms'
import { SlashCommand } from './slash'
import { LnMention } from './mention'
import { FileDrop } from './filedrop'
import { i18n } from '@/i18n'

const lowlight = createLowlight(common)

/** 顶层可出现的块类型：这些节点必须带稳定 id。 */
export const BLOCK_TYPES = ['paragraph', 'heading', 'bulletList', 'orderedList', 'taskList', 'blockquote', 'codeBlock', 'horizontalRule', 'table',
  'callout', 'toggle', 'columns', 'syncedBlock', 'mathBlock', 'mermaid', 'graphviz', 'mindmap', 'whiteboard', 'chart', 'childPage', 'childDatabase',
  'linkToPage', 'image', 'fileBlock', 'video', 'audio', 'bookmark', 'embed', 'tableOfContents', 'breadcrumb']

export function buildExtensions(opts: { placeholder?: boolean } = {}): AnyExtension[] {
  const t = i18n.global.t
  return [
    StarterKit.configure({
      heading: { levels: [1, 2, 3] },
      codeBlock: false,
      link: { openOnClick: false, autolink: true, HTMLAttributes: { rel: 'noopener noreferrer nofollow', target: '_blank' } },
      dropcursor: { color: 'var(--accent)', width: 2 },
      trailingNode: { node: 'paragraph' } as any,
    }),
    CodeBlockLowlight.configure({ lowlight, defaultLanguage: null }),
    TaskList,
    TaskItem.configure({ nested: true }),
    Table.configure({ resizable: true, cellMinWidth: 60 }),
    TableRow, TableHeader, TableCell,
    TextAlign.configure({ types: ['heading', 'paragraph'] }),
    Subscript, Superscript,
    TextColor, Highlight, CommentMark, BlockColor,
    Callout, Toggle, ToggleTitle, ToggleBody, Columns, Column, SyncedBlock,
    A.MathBlock, A.MathInline, A.Mermaid, A.Graphviz, A.Mindmap, A.Whiteboard, A.Chart, A.ChildPage, A.ChildDatabase, A.LinkToPage,
    A.Image, A.FileBlock, A.Video, A.Audio, A.Bookmark, A.Embed, A.TableOfContents, A.Breadcrumb,
    UniqueID.configure({ types: BLOCK_TYPES, attributeName: 'id', generateID: () => newBlockId() }),
    SlashCommand,
    LnMention,
    FileDrop,
    ...(opts.placeholder === false ? [] : [Placeholder.configure({
      includeChildren: false,
      placeholder: ({ node, editor }) => {
        if (node.type.name === 'heading') return t('editor.h' + node.attrs.level)
        if (node.type.name === 'toggleTitle') return t('editor.toggle')
        if (node.type.name === 'paragraph' && editor.isFocused) return t('page.placeholder')
        return ''
      },
    })]),
  ]
}

const ENC = '0123456789abcdefghjkmnpqrstvwxyz'
/** 与服务端 ULID 同格式的 26 位小写 id。 */
export function newBlockId(): string {
  let ts = Date.now()
  let out = ''
  for (let i = 0; i < 10; i++) { out = ENC[ts % 32] + out; ts = Math.floor(ts / 32) }
  const rnd = crypto.getRandomValues(new Uint8Array(16))
  for (let i = 0; i < 16; i++) out += ENC[rnd[i] % 32]
  return out
}
