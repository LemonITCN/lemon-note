import { reactive } from 'vue'
import { api } from '@/api/client'

export interface UserLite { id: string; username: string; display_name: string; avatar?: string }

const state = reactive<{ list: UserLite[]; map: Record<string, UserLite>; loaded: boolean }>({ list: [], map: {}, loaded: false })
let loading: Promise<void> | null = null

/** 系统用户目录（分享、提及、人员属性用）。 */
export function useUsers() {
  if (!state.loaded && !loading) {
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
