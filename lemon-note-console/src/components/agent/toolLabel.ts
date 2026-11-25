import { useWorkspace } from '@/stores/workspace'
import { i18n } from '@/i18n'

function pageTitle(id: string | undefined) {
  if (!id) return ''
  const p = useWorkspace().pages.get(id)
  return p ? p.title || i18n.global.t('common.untitled') : id.slice(-6)
}

function host(u: string) { try { return new URL(u).host } catch { return u } }

/** 工具调用的一句话描述（「读取 读书笔记」）。 */
export function toolLabel(name: string, args: any, title: string): string {
  const a = args || {}
  const zh = i18n.global.locale.value !== 'en'
  const q = (s: string) => (zh ? `「${s}」` : `"${s}"`)
  switch (name) {
    case 'search': return `${title} ${q(a.query || '')}`
    case 'read_page': return `${title} ${q(pageTitle(a.page_id))}`
    case 'edit_page': return `${title} ${q(pageTitle(a.page_id))}${a.edits?.length > 1 ? ` · ${a.edits.length}` : ''}`
    case 'write_page': return `${title} ${q(pageTitle(a.page_id))}`
    case 'update_page': return `${title} ${q(pageTitle(a.page_id))}`
    case 'create_pages': return `${title} ${(a.pages || []).map((p: any) => q(p.title || '')).join(' ')}`
    case 'grep_pages': return `${title} ${q(a.pattern || '')}`
    case 'list_pages': return a.parent_id ? `${title} ${q(pageTitle(a.parent_id))}` : title
    case 'move_pages':
    case 'delete_pages':
    case 'restore_pages': return `${title} ${(a.page_ids || []).map((id: string) => q(pageTitle(id))).join(' ')}`
    case 'duplicate_page':
    case 'page_history':
    case 'restore_page_version': return `${title} ${q(pageTitle(a.page_id))}`
    case 'web_fetch': return `${title} ${host(a.url || '')}`
    case 'web_search': return `${title} ${q(a.query || '')}`
    case 'task': return `${title} · ${a.description || ''}`
    case 'render_check': return `${title} · ${a.kind || ''}`
    default: return title || name
  }
}
