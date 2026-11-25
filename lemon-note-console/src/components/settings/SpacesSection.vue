<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useWorkspace } from '@/stores/workspace'
import { useSession } from '@/stores/session'
import { useUsers } from '@/lib/users'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { confirmDialog } from '@/lib/confirm'

const { t } = useI18n()
const ws = useWorkspace()
const s = useSession()
const users = useUsers()
const toast = useToast()
const sel = ref(ws.currentSpaceId)
const members = ref<any[]>([])
const name = ref('')
const instr = ref('')
const addUser = ref('')
const addRole = ref('editor')
const entry = computed(() => ws.spaces.find((x) => x.space.id === sel.value))
const manage = computed(() => ['owner', 'admin'].includes(entry.value?.role || ''))
const roles = ['viewer', 'commenter', 'editor', 'admin']

async function load() {
  if (!entry.value) return
  name.value = entry.value.space.name
  instr.value = (entry.value as any).settings?.agent_instructions || ''
  try { members.value = (await api.get(`/api/spaces/${sel.value}/members`)).members } catch {}
}
watch(sel, load, { immediate: true })

async function save() {
  try {
    await api.patch(`/api/spaces/${sel.value}`, { name: name.value, settings: { ...((entry.value as any)?.settings || {}), agent_instructions: instr.value } })
    await ws.loadSpaces()
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) }
}
async function setRole(uid: string, role: string) { try { await api.put(`/api/spaces/${sel.value}/members/${uid}`, { role }); await load() } catch (e) { toast.error(errorText(e)) } }
async function remove(uid: string) {
  if (!(await confirmDialog({ title: t('common.remove'), message: users.map[uid]?.display_name || uid, danger: true }))) return
  try { await api.del(`/api/spaces/${sel.value}/members/${uid}`); await load() } catch (e) { toast.error(errorText(e)) }
}
async function add() { if (addUser.value) { await setRole(addUser.value, addRole.value); addUser.value = '' } }
async function del() {
  if (!(await confirmDialog({ title: t('settings.deleteSpace'), message: entry.value?.space.name, danger: true }))) return
  try { await api.del(`/api/spaces/${sel.value}`); await ws.loadSpaces(); sel.value = ws.spaces[0]?.space.id } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <Section :title="t('settings.spaces')">
    <select v-model="sel" class="sel"><option v-for="x in ws.spaces" :key="x.space.id" :value="x.space.id">{{ x.space.name }}</option></select>
    <template v-if="entry">
      <TextField v-model="name" :label="t('common.name')" :disabled="!manage" />
      <div class="field">
        <span class="lbl">{{ t('settings.spaceInstructions') }}</span>
        <textarea v-model="instr" class="ta" rows="4" :disabled="!manage" :placeholder="t('settings.spaceInstructionsPh')" />
      </div>
      <div v-if="manage"><Btn variant="primary" @click="save">{{ t('common.save') }}</Btn></div>
    </template>
  </Section>
  <Section v-if="entry?.space.kind === 'shared'" :title="t('settings.members')">
    <div v-for="m in members" :key="m.user.id" class="mem">
      <span class="mn">{{ m.user.display_name }} <span class="mu">@{{ m.user.username }}</span></span>
      <span v-if="m.role === 'owner'" class="ro">{{ t('roles.owner') }}</span>
      <template v-else>
        <select class="sel sm" :value="m.role" :disabled="!manage" @change="setRole(m.user.id, ($event.target as HTMLSelectElement).value)"><option v-for="r in roles" :key="r" :value="r">{{ t('roles.' + r) }}</option></select>
        <Btn v-if="manage || m.user.id === s.user?.id" size="sm" variant="ghost" @click="remove(m.user.id)">{{ m.user.id === s.user?.id ? t('settings.leave') : t('common.remove') }}</Btn>
      </template>
    </div>
    <div v-if="manage" class="add">
      <select v-model="addUser" class="sel"><option value="" disabled>{{ t('settings.pickUser') }}</option><option v-for="u in users.list.filter((u) => !members.some((m) => m.user.id === u.id))" :key="u.id" :value="u.id">{{ u.display_name }} @{{ u.username }}</option></select>
      <select v-model="addRole" class="sel sm"><option v-for="r in roles" :key="r" :value="r">{{ t('roles.' + r) }}</option></select>
      <Btn size="sm" :disabled="!addUser" @click="add">{{ t('common.create') }}</Btn>
    </div>
    <div v-if="entry.role === 'owner'"><Btn size="sm" variant="danger" @click="del">{{ t('settings.deleteSpace') }}</Btn></div>
  </Section>
</template>

<style scoped>
.sel { height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); color: var(--text); max-width: 320px; }
.sel.sm { height: 28px; font-size: 12.5px; }
.field { display: flex; flex-direction: column; gap: 6px; }
.lbl { font-size: 12.5px; font-weight: 500; color: var(--text-2); }
.ta { width: 100%; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 8px 10px; background: var(--bg); outline: none; font-size: 13.5px; resize: vertical; }
.mem { display: flex; align-items: center; gap: 8px; }
.mn { flex: 1; font-size: 13.5px; }
.mu { color: var(--text-3); font-size: 12px; }
.ro { font-size: 12.5px; color: var(--text-3); }
.add { display: flex; gap: 6px; align-items: center; }
</style>
