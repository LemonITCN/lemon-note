<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import { useUI } from '@/stores/ui'
import { useSession } from '@/stores/session'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const ui = useUI()
const s = useSession()
const router = useRouter()

const userMenu = [
  { label: t('settings.account'), icon: 'user', run: () => ui.openSettings('account') },
  { label: t('settings.appearance'), icon: 'palette', run: () => ui.openSettings('appearance') },
  { separator: true },
  { label: s.user?.locale === 'en' ? 'Sign out' : '退出登录', icon: 'logout', run: async () => { await s.logout(); router.replace('/login') } },
]
</script>

<template>
  <nav class="rail">
    <RouterLink to="/" class="logo" :title="s.boot?.site_name"><Icon name="logo" :size="22" :stroke="1.5" /></RouterLink>
    <button class="agent-btn" :class="{ on: ui.agentVisible }" :title="t('nav.assistant')" @click="ui.toggleAgent()">
      <Icon name="assistant" :size="20" />
    </button>
    <IconBtn icon="search" :label="t('nav.search')" @click="ui.searchOpen = true" />
    <IconBtn icon="home" :label="t('nav.home')" @click="router.push('/')" />
    <IconBtn icon="sidebar" :label="ui.layout.sidebarOpen ? t('nav.collapse') : t('nav.expand')" :active="ui.layout.sidebarOpen" @click="ui.toggleSidebar()" />
    <div class="ln-spacer" />
    <IconBtn icon="settings" :label="t('nav.settings')" @click="ui.openSettings()" />
    <Menu :items="userMenu" side="right" align="end">
      <button class="avatar" :title="s.user?.display_name">{{ (s.user?.display_name || '?').slice(0, 1).toUpperCase() }}</button>
    </Menu>
  </nav>
</template>

<style scoped>
.rail { width: var(--rail-w); flex: none; display: flex; flex-direction: column; align-items: center; gap: 6px; padding: 10px 0 12px; background: var(--bg-soft); border-right: 1px solid var(--border); }
.logo { display: flex; align-items: center; justify-content: center; width: 34px; height: 34px; color: var(--text); margin-bottom: 8px; border-radius: 8px; }
.logo:hover { background: var(--bg-hover); }
.agent-btn { width: 34px; height: 34px; border-radius: 9px; border: 1px solid var(--border-strong); background: var(--bg); color: var(--text-2); display: flex; align-items: center; justify-content: center; margin-bottom: 6px; transition: all .15s; }
.agent-btn:hover { color: var(--accent); border-color: var(--accent); }
.agent-btn.on { background: var(--accent); color: #fff; border-color: var(--accent); }
.avatar { width: 28px; height: 28px; border-radius: 50%; border: 0; background: var(--accent-soft); color: var(--accent); font-weight: 600; font-size: 13px; }
</style>
