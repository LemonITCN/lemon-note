<script setup lang="ts">
import { onMounted, onBeforeUnmount, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Rail from '@/components/layout/Rail.vue'
import Sidebar from '@/components/layout/Sidebar.vue'
import AgentPanel from '@/components/agent/AgentPanel.vue'
import SearchDialog from '@/components/page/SearchDialog.vue'
import SettingsDialog from '@/components/settings/SettingsDialog.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import { useUI } from '@/stores/ui'
import { useWorkspace } from '@/stores/workspace'
import { realtime } from '@/lib/realtime'
import { startResize } from '@/lib/resize'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { installClientTools } from '@/lib/clientTools'
import { useRouter } from 'vue-router'

const { t } = useI18n()
const ui = useUI()
const ws = useWorkspace()
const toast = useToast()
const ready = ref(false)
const router = useRouter()
const offClient = installClientTools(router)

function onKey(e: KeyboardEvent) {
  const mod = e.metaKey || e.ctrlKey
  if (mod && e.key.toLowerCase() === 'k') { e.preventDefault(); ui.searchOpen = true }
  if (mod && e.key.toLowerCase() === 'j') { e.preventDefault(); ui.toggleAgent() }
  if (mod && e.key === '\\') { e.preventDefault(); ui.toggleSidebar() }
}

onMounted(async () => {
  realtime.start()
  window.addEventListener('keydown', onKey)
  try { await ws.init() } catch (e) { toast.error(errorText(e)) }
  ready.value = true
})
onBeforeUnmount(() => {
  realtime.stop()
  offClient()
  window.removeEventListener('keydown', onKey)
})
</script>

<template>
  <div class="shell" :class="{ mobile: ui.isMobile }">
    <Rail v-if="!ui.isMobile" />
    <template v-if="!ui.isMobile">
      <div v-show="ui.layout.agentOpen" class="agent-wrap" :style="{ width: ui.layout.agentWidth + 'px' }">
        <AgentPanel />
        <div class="resizer" @pointerdown="startResize($event, () => ui.layout.agentWidth, (w) => (ui.layout.agentWidth = w), 320, 760)" />
      </div>
      <div v-show="ui.layout.sidebarOpen" class="side-wrap" :style="{ width: ui.layout.sidebarWidth + 'px' }">
        <Sidebar v-if="ready" />
        <div class="resizer" @pointerdown="startResize($event, () => ui.layout.sidebarWidth, (w) => (ui.layout.sidebarWidth = w), 200, 480)" />
      </div>
    </template>
    <template v-else>
      <Transition name="drawer"><div v-if="ui.mobileSidebar" class="drawer left" @click.self="ui.mobileSidebar = false"><div class="drawer-body"><Sidebar v-if="ready" /></div></div></Transition>
      <Transition name="drawer"><div v-if="ui.mobileAgent" class="drawer full"><AgentPanel /></div></Transition>
    </template>
    <main class="main">
      <div v-if="ui.isMobile" class="mobile-bar">
        <IconBtn icon="menu" :label="t('nav.expand')" @click="ui.toggleSidebar(true)" />
        <span class="ln-spacer" />
        <IconBtn icon="search" :label="t('nav.search')" @click="ui.searchOpen = true" />
        <IconBtn icon="assistant" :label="t('nav.assistant')" @click="ui.toggleAgent(true)" />
      </div>
      <RouterView v-if="ready" v-slot="{ Component, route }">
        <component :is="Component" :key="route.params.id || route.name" />
      </RouterView>
    </main>
    <SearchDialog />
    <SettingsDialog />
  </div>
</template>

<style scoped>
.shell { height: 100%; display: flex; overflow: hidden; background: var(--bg); }
.agent-wrap, .side-wrap { position: relative; flex: none; height: 100%; border-right: 1px solid var(--border); min-width: 0; }
.agent-wrap { background: var(--bg); }
.resizer { position: absolute; top: 0; right: -3px; width: 6px; height: 100%; cursor: col-resize; z-index: 5; }
.resizer:hover { background: linear-gradient(to right, transparent 2px, var(--accent) 2px, var(--accent) 4px, transparent 4px); }
.main { flex: 1; min-width: 0; height: 100%; overflow: hidden; display: flex; flex-direction: column; }
.mobile-bar { display: flex; align-items: center; gap: 4px; height: 44px; padding: 0 8px; border-bottom: 1px solid var(--border); flex: none; }
.drawer { position: fixed; inset: 0; z-index: 500; }
.drawer.left { background: var(--overlay); }
.drawer-body { width: min(320px, 86vw); height: 100%; background: var(--bg-soft); box-shadow: var(--shadow-pop); }
.drawer.full { background: var(--bg); }
.drawer-enter-from, .drawer-leave-to { opacity: 0; }
.drawer-enter-active, .drawer-leave-active { transition: opacity .18s; }
</style>
