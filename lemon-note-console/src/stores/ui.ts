import { defineStore } from 'pinia'
import { ref, watch, computed } from 'vue'
import { useMediaQuery } from '@vueuse/core'

export type ThemeMode = 'light' | 'dark' | 'system'
export interface Appearance { theme: ThemeMode; accent: string; font: 'default' | 'serif' | 'mono'; density: 'compact' | 'default' | 'comfortable' }

const defaults: Appearance = { theme: 'system', accent: 'blue', font: 'default', density: 'default' }

function load<T>(key: string, def: T): T {
  try {
    const v = localStorage.getItem(key)
    return v ? { ...def, ...JSON.parse(v) } : def
  } catch { return def }
}

function save(key: string, v: unknown) {
  try { localStorage.setItem(key, JSON.stringify(v)) } catch {}
}

export const useUI = defineStore('ui', () => {
  const appearance = ref<Appearance>(load('ln.appearance', defaults))
  const layout = ref(load('ln.layout', { sidebarWidth: 260, sidebarOpen: true, agentWidth: 400, agentOpen: false }))
  const prefersDark = useMediaQuery('(prefers-color-scheme: dark)')
  const isMobile = useMediaQuery('(max-width: 760px)')
  const mobileSidebar = ref(false)
  const mobileAgent = ref(false)
  const searchOpen = ref(false)
  const settingsOpen = ref(false)
  const settingsSection = ref('account')

  const effectiveTheme = computed(() => appearance.value.theme === 'system' ? (prefersDark.value ? 'dark' : 'light') : appearance.value.theme)

  function apply() {
    const el = document.documentElement
    el.dataset.theme = effectiveTheme.value
    el.dataset.accent = appearance.value.accent
    el.dataset.font = appearance.value.font
    el.dataset.density = appearance.value.density
  }
  watch([appearance, effectiveTheme], () => { apply(); save('ln.appearance', appearance.value) }, { deep: true, immediate: true })
  watch(layout, () => save('ln.layout', layout.value), { deep: true })

  /** 用服务器上保存的偏好覆盖本地（登录后调用）。 */
  function adopt(prefs: any) {
    if (prefs?.appearance) appearance.value = { ...defaults, ...prefs.appearance }
  }

  const agentVisible = computed(() => (isMobile.value ? mobileAgent.value : layout.value.agentOpen))
  function toggleAgent(open?: boolean) {
    if (isMobile.value) mobileAgent.value = open ?? !mobileAgent.value
    else layout.value.agentOpen = open ?? !layout.value.agentOpen
  }
  function toggleSidebar(open?: boolean) {
    if (isMobile.value) mobileSidebar.value = open ?? !mobileSidebar.value
    else layout.value.sidebarOpen = open ?? !layout.value.sidebarOpen
  }
  function openSettings(section = 'account') {
    settingsSection.value = section
    settingsOpen.value = true
  }

  return { appearance, layout, isMobile, mobileSidebar, mobileAgent, searchOpen, settingsOpen, settingsSection, effectiveTheme,
    agentVisible, adopt, toggleAgent, toggleSidebar, openSettings }
})
