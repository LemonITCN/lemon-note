import type { Editor } from '@tiptap/core'

export interface PageContext { pageId: string; spaceId: string }

/** 编辑器所属页面（节点视图上传附件、算面包屑时用）。 */
export function pageContext(editor: Editor): PageContext | undefined {
  return (editor.storage as any).lnPage as PageContext | undefined
}
