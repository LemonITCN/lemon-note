import { reactive } from 'vue'
import { api } from '@/api/client'
import { isPublic } from './publicCtx'

export interface UserLite { id: string; username: string; display_name: string; avatar?: string }

const state = reactive<{ list: UserLite[]; map: Record<string, UserLite>; loaded: boolean }>({ list: [], map: {}, loaded: false })
let loading: Promise<void> | null = null

/** 系统用户目录（分享、提及、人员属性用）。 */
export function useUsers() {
  if (!state.loaded && !loading && !isPublic()) {
    loading = api.get('/api/users').then((r) => {
      state.list = r.users || []
      for (const u of state.list) state.map[u.id] = u
      state.loaded = true
    }).catch(() => {}).finally(() => { loading = null })
  }
  return state
}

export function userName(id: string) {
  return useUsers().map[id]?.display_name || id
}

/** 公开分享页没有用户目录，用接口随数据带回的用户名补上。 */
export function seedUsers(m: Record<string, { id: string; display_name: string; avatar?: string }> | undefined) {
  for (const u of Object.values(m || {})) if (!state.map[u.id]) state.map[u.id] = { id: u.id, username: '', display_name: u.display_name, avatar: u.avatar }
}
