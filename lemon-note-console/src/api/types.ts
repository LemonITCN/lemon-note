export interface User {
  id: string
  username: string
  display_name: string
  email?: string
  avatar: string
  role: 'admin' | 'member'
  status: 'active' | 'disabled'
  locale: string
  totp_enabled: boolean
  prefs: Record<string, any>
  created_at: number
  last_login_at?: number
}

export interface Space {
  id: string
  kind: 'personal' | 'shared'
  name: string
  icon: string
  owner_id: string
  created_at: number
}

export type Role = '' | 'viewer' | 'commenter' | 'editor' | 'admin' | 'owner'

export interface PageMeta {
  id: string
  space_id: string
  parent_id?: string
  database_id?: string
  kind: 'page' | 'database'
  title: string
  icon?: string
  position: string
  inline?: boolean
  is_template?: boolean
  trashed_at?: number
  trash_root?: string
  created_at: number
  created_by: string
  updated_at: number
  updated_by: string
}

export interface PageFlags {
  full_width?: boolean
  small_text?: boolean
  font?: '' | 'default' | 'serif' | 'mono'
  locked?: boolean
  inline?: boolean
  hide_title?: boolean
  cover_offset?: number
}

export interface Page {
  id: string
  space_id: string
  parent_id: string | null
  database_id: string | null
  kind: 'page' | 'database'
  title: string
  icon: string
  cover: string
  position: string
  is_template: boolean
  content_rev: number
  created_at: number
  created_by: string
  updated_at: number
  updated_by: string
  trashed_at?: number | null
}

export interface BlockDoc {
  id: string
  type: string
  position: string
  data: any
  version: number
}

export interface PageResponse {
  page: Page
  flags: PageFlags
  props: Record<string, any>
  blocks: BlockDoc[]
  role: Role
  breadcrumbs: PageMeta[]
  space: Space
  children: PageMeta[]
}

export function roleAtLeast(r: Role, need: Role) {
  const rank: Record<string, number> = { '': 0, viewer: 1, commenter: 2, editor: 3, admin: 4, owner: 5 }
  return (rank[r] ?? 0) >= (rank[need] ?? 0)
}
