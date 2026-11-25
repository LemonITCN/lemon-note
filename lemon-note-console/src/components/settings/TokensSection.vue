<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import Modal from '@/components/ui/Modal.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import { api } from '@/api/client'
import { useWorkspace } from '@/stores/workspace'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { formatRelative } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'

const { t } = useI18n()
const ws = useWorkspace()
const toast = useToast()
const list = ref<any[]>([])
const open = ref(false)
const name = ref('')
const write = ref(false)
const spaces = ref<string[]>([])
const created = ref('')
const mcpURL = computed(() => location.origin + '/mcp')
const cmd = computed(() => `claude mcp add --transport http lemon-note ${mcpURL.value} --header "Authorization: Bearer ${created.value || '<令牌>'}"`)

async function load() { try { list.value = (await api.get('/api/me/tokens')).tokens } catch {} }
onMounted(load)

async function create() {
  try {
    const r = await api.post('/api/me/tokens', { name: name.value, write: write.value, spaces: spaces.value })
    created.value = r.token
    name.value = ''
    await load()
  } catch (e) { toast.error(errorText(e)) }
}

async function revoke(id: string, n: string) {
  if (!(await confirmDialog({ title: t('settings.revoke'), message: n, danger: true }))) return
  try { await api.del(`/api/me/tokens/${id}`); await load() } catch (e) { toast.error(errorText(e)) }
}

function copy(s: string) { navigator.clipboard?.writeText(s); toast.success(t('common.copied')) }
function spaceName(id: string) { return ws.spaces.find((s) => s.space.id === id)?.space.name || id }
</script>

<template>
  <Section :title="t('settings.tokens')" :tip="t('settings.mcpTip')">
    <div class="mcp"><span class="ml">MCP</span><code>{{ mcpURL }}</code><IconBtn icon="copy" :size="15" :label="t('common.copy')" @click="copy(mcpURL)" /></div>
    <div v-for="x in list" :key="x.id" class="tok">
      <div class="ti">
        <div class="tn">{{ x.name }}<span class="badge">{{ x.write ? t('settings.readWrite') : t('settings.readOnly') }}</span></div>
        <div class="tm">{{ x.prefix }}… · {{ x.spaces?.length ? x.spaces.map(spaceName).join('、') : t('settings.allSpaces') }} · {{ x.last_used_at ? formatRelative(x.last_used_at) : t('settings.neverUsed') }}</div>
      </div>
      <Btn size="sm" variant="ghost" @click="revoke(x.id, x.name)">{{ t('settings.revoke') }}</Btn>
    </div>
    <div><Btn size="sm" variant="primary" icon="plus" @click="open = true; created = ''">{{ t('settings.newToken') }}</Btn></div>
  </Section>
  <Modal v-model:open="open" :title="t('settings.newToken')" :width="520">
    <div v-if="!created" class="form">
      <TextField v-model="name" :label="t('settings.tokenName')" placeholder="Claude Code" autofocus />
      <label class="sw"><Switch v-model="write" />{{ t('settings.tokenWrite') }}</label>
      <div class="field">
        <span class="lbl">{{ t('settings.tokenSpaces') }}</span>
        <label v-for="s in ws.spaces" :key="s.space.id" class="chk"><input v-model="spaces" type="checkbox" :value="s.space.id" />{{ s.space.name }}</label>
      </div>
      <Btn variant="primary" :disabled="!name.trim()" @click="create">{{ t('common.create') }}</Btn>
    </div>
    <div v-else class="form">
      <p class="warn">{{ t('settings.tokenCreated') }}</p>
      <div class="secret"><code>{{ created }}</code><IconBtn icon="copy" :label="t('common.copy')" @click="copy(created)" /></div>
      <span class="lbl">Claude Code</span>
      <div class="secret"><code class="cmd">{{ cmd }}</code><IconBtn icon="copy" :label="t('common.copy')" @click="copy(cmd)" /></div>
      <Btn variant="primary" @click="open = false">{{ t('common.done') }}</Btn>
    </div>
  </Modal>
</template>

<style scoped>
.mcp { display: flex; align-items: center; gap: 8px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 6px 6px 6px 10px; }
.ml { font-size: 11.5px; font-weight: 600; color: var(--text-3); }
.mcp code { flex: 1; font-family: var(--font-mono); font-size: 12.5px; }
.tok { display: flex; align-items: center; gap: 10px; }
.ti { flex: 1; min-width: 0; }
.tn { font-size: 13.5px; display: flex; gap: 6px; align-items: center; }
.badge { font-size: 11px; background: var(--bg-hover); border-radius: 4px; padding: 0 5px; color: var(--text-2); }
.tm { font-size: 12px; color: var(--text-3); }
.form { display: flex; flex-direction: column; gap: 12px; }
.sw, .chk { display: flex; align-items: center; gap: 8px; font-size: 13.5px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.lbl { font-size: 12.5px; font-weight: 500; color: var(--text-2); }
.warn { margin: 0; color: var(--warning); font-size: 13px; }
.secret { display: flex; align-items: flex-start; gap: 6px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px; }
.secret code { flex: 1; font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
</style>
