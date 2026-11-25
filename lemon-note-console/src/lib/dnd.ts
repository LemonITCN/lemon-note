import { reactive } from 'vue'

/** 拖拽引用的数据类型：页面树、编辑器段落、数据库都用它把内容拖进智能体聊天。 */
export const REF_MIME = 'application/x-lemon-ref'

export const dragState = reactive({ pageId: '' })
