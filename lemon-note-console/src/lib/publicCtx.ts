// 公开分享页的匿名访问上下文：接口路径改写、附件地址、站内跳转都据此切换。
import { reactive } from 'vue'

export interface PublicTreeItem { id: string; parent_id?: string; title: string; icon?: string; kind: string; position: string }

export const pub = reactive<{ token: string; rootId: string; tree: PublicTreeItem[] }>({ token: '', rootId: '', tree: [] })

export function isPublic() { return !!pub.token }

/** 匿名访问时把登录接口改写成公开接口：/api/pages/x → /api/public/<令牌>/pages/x。 */
export function publicPath(path: string): string {
  if (!pub.token || !path.startsWith('/api/') || path.startsWith('/api/public/')) return path
  return '/api/public/' + pub.token + path.slice(4)
}

export function enterPublic(token: string) {
  if (pub.token !== token) { pub.token = token; pub.rootId = ''; pub.tree = [] }
}

export function leavePublic() { pub.token = ''; pub.rootId = ''; pub.tree = [] }

export function inPublicTree(id: string) { return pub.tree.some((p) => p.id === id) }
