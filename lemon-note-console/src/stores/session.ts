import { defineStore } from 'pinia'
import { ref } from 'vue'
import { api, setCsrf } from '@/api/client'
import type { User } from '@/api/types'
import { setLocale } from '@/i18n'
import { useUI } from './ui'

export interface BootInfo {
  mode: 'setup' | 'normal'
  version: string
  site_name?: string
  default_locale?: string
  allow_registration?: boolean
  public_sharing?: boolean
  data_dir?: string
  engines?: string[]
  csrf?: string
  user?: User
  mfa_pending?: boolean
  mfa_setup_required?: boolean
}

export const useSession = defineStore('session', () => {
  const boot = ref<BootInfo | null>(null)
  const user = ref<User | null>(null)
  const mfaPending = ref(false)
  const mfaSetupRequired = ref(false)

  async function load() {
    const b = await api.get<BootInfo>('/api/bootstrap')
    boot.value = b
    setCsrf(b.csrf)
    mfaPending.value = !!b.mfa_pending
    mfaSetupRequired.value = !!b.mfa_setup_required
    if (b.user) adoptUser(b.user)
    else if (b.default_locale && !localStorage.getItem('ln.locale')) setLocale(b.default_locale)
    if (b.site_name) document.title = b.site_name
    return b
  }

  function adoptUser(u: User) {
    user.value = u
    setLocale(u.locale)
    useUI().adopt(u.prefs)
  }

  async function login(username: string, password: string) {
    const r = await api.post<any>('/api/auth/login', { username, password })
    setCsrf(r.csrf)
    if (r.mfa_required) {
      mfaPending.value = true
      return 'mfa'
    }
    mfaSetupRequired.value = !!r.mfa_setup_required
    adoptUser(r.user)
    return 'ok'
  }

  async function verifyMFA(code: string) {
    const r = await api.post<any>('/api/auth/mfa', { code })
    setCsrf(r.csrf)
    mfaPending.value = false
    adoptUser(r.user)
  }

  async function logout() {
    try { await api.post('/api/auth/logout') } catch {}
    user.value = null
    setCsrf('')
  }

  async function savePrefs(patch: Record<string, any>) {
    if (!user.value) return
    const prefs = { ...(user.value.prefs || {}), ...patch }
    const r = await api.put<any>('/api/me/prefs', prefs)
    user.value = r.user
  }

  return { boot, user, mfaPending, mfaSetupRequired, load, login, verifyMFA, logout, adoptUser, savePrefs }
})
