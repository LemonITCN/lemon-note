// 数据库：类型与接口封装。
import { api } from '@/api/client'

export interface DbOption { id: string; name: string; color?: string; group?: string }
export interface DbProperty {
  id: string; name: string; type: string; options?: DbOption[]; format?: string; expression?: string
  database_id?: string; two_way?: boolean; related_prop?: string; relation?: string; target?: string; function?: string; prefix?: string
}
export interface DbSchema { properties: DbProperty[] }
export interface Filter { and?: Filter[]; or?: Filter[]; prop?: string; op?: string; value?: any }
export interface ViewConfig {
  filter?: Filter; sorts?: { prop: string; dir: 'asc' | 'desc' }[]; group_by?: { prop: string; by?: string; hide_empty?: boolean; order?: string[]; hidden?: string[] }
  visible?: string[]; widths?: Record<string, number>; card_cover?: string; card_size?: string; date_prop?: string; end_prop?: string; wrap?: boolean
  chart?: any; calculate?: Record<string, string>
}
export interface DbView { id: string; name: string; type: string; position: string; host_id?: string; config: ViewConfig }
export interface DbRow { id: string; title: string; icon?: string; position: string; props: Record<string, any>; computed?: Record<string, any>; created_at: number; created_by: string; updated_at: number; updated_by: string }
export interface DbGroup { key: string; label: string; color?: string; rows: DbRow[]; count: number }
export interface QueryResult { rows: DbRow[]; groups?: DbGroup[]; total: number; calcs?: Record<string, any> }

export const PROP_TYPES = ['text', 'number', 'select', 'multi_select', 'status', 'date', 'person', 'files', 'checkbox', 'url', 'email', 'phone', 'formula', 'relation', 'rollup', 'created_time', 'created_by', 'last_edited_time', 'last_edited_by', 'unique_id'] as const

export const TYPE_ICONS: Record<string, string> = {
  title: 'text', text: 'text', number: 'number', select: 'select', multi_select: 'tags', status: 'status', date: 'calendar', person: 'person', files: 'paperclip',
  checkbox: 'checkbox', url: 'link', email: 'mail', phone: 'phone', formula: 'formula', relation: 'relation', rollup: 'rollup', created_time: 'clock',
  created_by: 'person', last_edited_time: 'clock', last_edited_by: 'person', unique_id: 'id',
}

export const VIEW_ICONS: Record<string, string> = { table: 'table', board: 'board', list: 'list', gallery: 'gallery', calendar: 'calendar', timeline: 'timeline', chart: 'chart' }

export const COMPUTED = new Set(['formula', 'rollup', 'created_time', 'created_by', 'last_edited_time', 'last_edited_by', 'unique_id'])

export const dbApi = {
  info: (id: string) => api.get<{ page: any; schema: DbSchema; views: DbView[]; role: string }>(`/api/databases/${id}`),
  create: (body: any) => api.post<{ page: any; views: DbView[] }>('/api/databases', body),
  saveSchema: (id: string, schema: DbSchema) => api.put<{ schema: DbSchema }>(`/api/databases/${id}/schema`, schema),
  query: (id: string, body: any) => api.post<QueryResult>(`/api/databases/${id}/query`, body),
  addRow: (id: string, body: any) => api.post<{ row: DbRow }>(`/api/databases/${id}/rows`, body),
  patchRow: (id: string, body: any) => api.patch<{ row: DbRow }>(`/api/rows/${id}`, body),
  addView: (id: string, body: any) => api.post<{ view: DbView }>(`/api/databases/${id}/views`, body),
  patchView: (id: string, body: any) => api.patch<{ view: DbView }>(`/api/views/${id}`, body),
  delView: (id: string) => api.del(`/api/views/${id}`),
}

export function newPropId() { return 'p' + Math.random().toString(36).slice(2, 8) }
export function newOptionId() { return 'o' + Math.random().toString(36).slice(2, 8) }

export function formatNumber(v: any, fmt?: string) {
  if (v === null || v === undefined || v === '') return ''
  const n = Number(v)
  if (Number.isNaN(n)) return String(v)
  switch (fmt) {
    case 'comma': return n.toLocaleString()
    case 'percent': return (n * 100).toFixed(1).replace(/\.0$/, '') + '%'
    case 'yuan': return '¥' + n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
    case 'dollar': return '$' + n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
    case 'euro': return '€' + n.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })
  }
  return Number.isInteger(n) ? String(n) : String(Math.round(n * 1e6) / 1e6)
}

export function dateText(v: any) {
  if (!v) return ''
  if (typeof v === 'string') return v
  return v.end ? `${v.start} → ${v.end}` : v.start || ''
}
