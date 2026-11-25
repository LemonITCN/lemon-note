import { reactive } from 'vue'

/** 聊天引用：页面、数据库、数据库行、选中的段落、附件。 */
export interface ChatRef {
  kind: 'page' | 'database' | 'row' | 'selection' | 'file' | 'view'
  id: string
  title: string
  icon?: string
  pageId?: string
  blockIds?: string[]
  text?: string
  viewId?: string
}

export const chatInbox = reactive<{ pending: ChatRef[] }>({ pending: [] })

/** 把内容添加到智能体聊天（面板会消费）。 */
export function addToChat(ref: ChatRef) {
  chatInbox.pending.push(ref)
}
