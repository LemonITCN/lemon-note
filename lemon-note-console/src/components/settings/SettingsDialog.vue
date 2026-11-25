<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import AccountSection from './AccountSection.vue'
import AppearanceSection from './AppearanceSection.vue'
import SecuritySection from './SecuritySection.vue'
import TokensSection from './TokensSection.vue'
import AgentSection from './AgentSection.vue'
import SpacesSection from './SpacesSection.vue'
import AdminUsers from './AdminUsers.vue'
import AdminSystem from './AdminSystem.vue'
import AdminAI from './AdminAI.vue'
import AdminAudit from './AdminAudit.vue'
import AdminStatus from './AdminStatus.vue'
import BackupSection from './BackupSection.vue'
import { useUI } from '@/stores/ui'
import { useSession } from '@/stores/session'

const { t } = useI18n()
const ui = useUI()
const s = useSession()
const isAdmin = computed(() => s.user?.role === 'admin')
const groups = computed(() => [
  { title: s.user?.display_name || '', items: [['account', 'user'], ['appearance', 'palette'], ['security', 'shield'], ['tokens', 'key'], ['agent', 'assistant'], ['spaces', 'users']] },
  ...(isAdmin.value ? [{ title: t('settings.admin'), items: [['users', 'users'], ['system', 'settings'], ['ai', 'sparkle'], ['backup', 'cloud-up'], ['audit', 'history'], ['status', 'server']] }] : []),
])
const comps: Record<string, any> = { account: AccountSection, appearance: AppearanceSection, security: SecuritySection, tokens: TokensSection, agent: AgentSection, spaces: SpacesSection,
  users: AdminUsers, system: AdminSystem, ai: AdminAI, backup: BackupSection, audit: AdminAudit, status: AdminStatus }
const labels: Record<string, string> = { agent: 'settings.agentInstructions' }
</script>

<template>
  <DialogRoot v-model:open="ui.settingsOpen">
    <DialogPortal>
      <DialogOverlay class="ln-settings-overlay" />
      <DialogContent class="ln-settings" :aria-describedby="undefined">
        <nav class="nav">
          <DialogTitle class="nt">{{ t('settings.title') }}</DialogTitle>
          <template v-for="g in groups" :key="g.title">
            <div class="ng">{{ g.title }}</div>
            <button v-for="[k, ic] in g.items" :key="k" class="ni" :class="{ on: ui.settingsSection === k }" @click="ui.settingsSection = k"><Icon :name="ic" :size="16" />{{ t(labels[k] || 'settings.' + k) }}</button>
          </template>
        </nav>
        <div class="main">
          <div class="mh"><span>{{ t(labels[ui.settingsSection] || 'settings.' + ui.settingsSection) }}</span><span class="ln-spacer" /><IconBtn icon="close" :label="t('common.close')" @click="ui.settingsOpen = false" /></div>
          <div class="mb"><component :is="comps[ui.settingsSection] || AccountSection" :key="ui.settingsSection" /></div>
        </div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
.ln-settings-overlay { position: fixed; inset: 0; background: var(--overlay); z-index: 1000; }
.ln-settings { position: fixed; left: 50%; top: 50%; transform: translate(-50%, -50%); width: min(980px, calc(100vw - 32px)); height: min(720px, calc(100vh - 48px)); background: var(--bg-elev);
  border-radius: var(--radius-l); box-shadow: var(--shadow-pop); z-index: 1001; display: flex; overflow: hidden; outline: none; }
.ln-settings .nav { width: 220px; background: var(--bg-soft); border-right: 1px solid var(--border); padding: 14px 8px; overflow-y: auto; flex: none; }
.ln-settings .nt { font-size: 15px; font-weight: 600; margin: 0 8px 12px; }
.ln-settings .ng { font-size: 11.5px; color: var(--text-3); padding: 10px 8px 4px; font-weight: 500; }
.ln-settings .ni { display: flex; align-items: center; gap: 8px; width: 100%; height: 30px; border: 0; background: none; border-radius: 5px; padding: 0 8px; color: var(--text-2); text-align: left; font-size: 13.5px; }
.ln-settings .ni:hover { background: var(--bg-hover); }
.ln-settings .ni.on { background: var(--bg-active); color: var(--text); font-weight: 500; }
.ln-settings .main { flex: 1; display: flex; flex-direction: column; min-width: 0; }
.ln-settings .mh { display: flex; align-items: center; height: 52px; padding: 0 12px 0 28px; border-bottom: 1px solid var(--border); font-weight: 600; flex: none; }
.ln-settings .mb { flex: 1; overflow-y: auto; padding: 22px 28px; }
@media (max-width: 760px) {
  .ln-settings { width: 100vw; height: 100vh; border-radius: 0; flex-direction: column; }
  .ln-settings .nav { width: 100%; height: auto; max-height: 34vh; border-right: 0; border-bottom: 1px solid var(--border); }
  .ln-settings .mb { padding: 16px; }
}
</style>
