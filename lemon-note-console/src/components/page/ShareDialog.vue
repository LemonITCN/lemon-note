<script setup lang="ts">
// 分享：邀请本系统用户（按页面授予角色，子页面继承）、发布到网页（匿名只读公开链接）。
import { ref, computed, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Segmented from '@/components/ui/Segmented.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { useSession } from '@/stores/session'
import { useUsers } from '@/lib/users'
import { errorText } from '@/i18n'
import { formatDate } from '@/lib/time'
import { confirmDialog } from '@/lib/confirm'
import { useWorkspace } from '@/stores/workspace'
import { useRouter } from 'vue-router'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ pageId: string; pageTitle: string; role: string }>()
const { t } = useI18n()
const toast = useToast()
const session = useSession()
const users = useUsers()
const ws = useWorkspace()
const router = useRouter()

interface ShareUser { share_id: string; user: { id: string; username: string; display_name: string }; role: string; page_id: string; page_title: string; inherited: boolean }
interface PublicLink { id: string; token: string; path: string; page_id: string; page_title: string; include_children: boolean; allow_index: boolean; has_password: boolean; expires_at: number | null; expired: boolean }
interface Sharing { role: string; can_manage: boolean; public_enabled: boolean; space: { id: string; name: string; kind: string; members: number }; users: ShareUser[]; public: PublicLink | null; inherited_public: PublicLink[] }

const tab = ref('invite')
const data = ref<Sharing | null>(null)
const busy = ref(false)
const query = ref('')
const picked = ref<{ id: string; display_name: string; username: string } | null>(null)
const pickRole = ref('editor')
const pw = ref('')
const pwEditing = ref(false)
const roleKeys = ['admin', 'editor', 'commenter', 'viewer']

async function load() {
  try { data.value = await api.get(`/api/pages/${props.pageId}/sharing`) } catch (e) { toast.error(errorText(e)) }
}
watch(open, (v) => { if (v) { data.value = null; tab.value = 'invite'; pwEditing.value = false; pw.value = ''; load() } }, { immediate: true })

const tabs = computed(() => [{ value: 'invite', label: t('share.invite') }, { value: 'publish', label: t('share.publish') }])
const manage = computed(() => !!data.value?.can_manage)
const me = computed(() => session.user?.id)
const direct = computed(() => (data.value?.users || []).filter((u) => !u.inherited))
const inherited = computed(() => (data.value?.users || []).filter((u) => u.inherited))
const candidates = computed(() => {
  const q = query.value.trim().toLowerCase()
  if (!q) return []
  const taken = new Set(direct.value.map((u) => u.user.id))
  return users.list.filter((u) => u.id !== me.value && !taken.has(u.id) && (u.display_name.toLowerCase().includes(q) || u.username.toLowerCase().includes(q))).slice(0, 8)
})
const link = computed(() => (data.value?.public?.path ? location.origin + data.value.public.path : ''))

async function run(fn: () => Promise<Sharing>) {
  busy.value = true
  try { data.value = await fn() } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
const base = () => `/api/pages/${props.pageId}/sharing`
function invite() {
  if (!picked.value) return
  const uid = picked.value.id
  run(() => api.put(`${base()}/users/${uid}`, { role: pickRole.value })).then(() => { picked.value = null; query.value = '' })
}
function setRole(uid: string, role: string) { run(() => api.put(`${base()}/users/${uid}`, { role })) }
async function remove(u: ShareUser) {
  if (u.user.id !== me.value) { run(() => api.del(`${base()}/users/${u.user.id}`)); return }
  if (!(await confirmDialog({ title: t('share.leave'), message: props.pageTitle || t('common.untitled'), danger: true }))) return
  try {
    const r = await api.del(`${base()}/users/${u.user.id}`)
    if (r?.left) { open.value = false; ws.loadSide(); router.push('/') } else data.value = r
  } catch (e) { toast.error(errorText(e)) }
}
function publish(patch: Record<string, any> = {}) { return run(() => api.put(`${base()}/public`, patch)) }
async function unpublish() {
  if (!(await confirmDialog({ title: t('share.unpublish'), message: t('share.unpublishMsg'), danger: true }))) return
  run(() => api.del(`${base()}/public`))
}
async function rotate() {
  if (!(await confirmDialog({ title: t('share.rotate'), message: t('share.rotateTip'), danger: true }))) return
  run(() => api.post(`${base()}/public/rotate`))
}
async function savePassword() {
  if (pw.value.length < 4) { toast.error(t('share.passwordShort')); return }
  await publish({ password: pw.value })
  pw.value = ''
  pwEditing.value = false
}
const expiry = computed({
  get: () => {
    const e = data.value?.public?.expires_at
    if (!e) return 'never'
    return 'custom'
  },
  set: (v: string) => {
    if (v === 'never') publish({ set_expiry: true, expires_at: null })
    else if (v !== 'custom') publish({ set_expiry: true, expires_at: Date.now() + Number(v) * 86400000 })
  },
})
function copy() { navigator.clipboard?.writeText(link.value).then(() => toast.success(t('common.copied'))) }
function initial(name: string) { return (name || '?').slice(0, 1).toUpperCase() }
</script>

<template>
  <Modal v-model:open="open" :title="t('share.title')" :width="540">
    <div class="sh">
      <Segmented v-model="tab" :options="tabs" />
      <div v-if="!data" class="ld" />
      <template v-else-if="tab === 'invite'">
        <div v-if="manage" class="inv">
          <div class="pick">
            <div v-if="picked" class="chip"><span class="av sm">{{ initial(picked.display_name) }}</span>{{ picked.display_name }}<button class="x" :aria-label="t('common.remove')" @click="picked = null"><Icon name="close" :size="12" /></button></div>
            <input v-else v-model="query" class="q" :placeholder="t('share.invitePh')" />
            <div v-if="!picked && candidates.length" class="sugg">
              <button v-for="u in candidates" :key="u.id" class="opt" @click="picked = u; query = ''"><span class="av sm">{{ initial(u.display_name) }}</span>{{ u.display_name }}<span class="un">@{{ u.username }}</span></button>
            </div>
          </div>
          <select v-model="pickRole" class="sel"><option v-for="r in roleKeys" :key="r" :value="r">{{ t('share.roles.' + r) }}</option></select>
          <Btn variant="primary" :disabled="!picked" :loading="busy" @click="invite">{{ t('share.inviteBtn') }}</Btn>
        </div>
        <div class="list">
          <div class="row">
            <span class="av sp"><Icon name="users" :size="15" /></span>
            <div class="who"><div class="nm">{{ data.space.kind === 'personal' ? t('share.personalSpace') : data.space.name }}</div><div class="sub">{{ data.space.kind === 'personal' ? t('share.onlyYou') : t('share.spaceMembers', { n: data.space.members }) }}</div></div>
            <span class="rl">{{ t('share.spaceAccess') }}</span>
          </div>
          <div v-for="u in direct" :key="u.share_id" class="row">
            <span class="av">{{ initial(u.user.display_name) }}</span>
            <div class="who"><div class="nm">{{ u.user.display_name }}<span v-if="u.user.id === me" class="you">{{ t('share.you') }}</span></div><div class="sub">@{{ u.user.username }}</div></div>
            <select v-if="manage" class="sel sm" :value="u.role" @change="setRole(u.user.id, ($event.target as HTMLSelectElement).value)"><option v-for="r in roleKeys" :key="r" :value="r">{{ t('share.roles.' + r) }}</option></select>
            <span v-else class="rl">{{ t('share.roles.' + u.role) }}</span>
            <IconBtn v-if="manage || u.user.id === me" icon="close" :size="15" :label="u.user.id === me ? t('share.leave') : t('share.removeAccess')" @click="remove(u)" />
          </div>
          <div v-for="u in inherited" :key="u.share_id" class="row inh">
            <span class="av">{{ initial(u.user.display_name) }}</span>
            <div class="who"><div class="nm">{{ u.user.display_name }}</div><div class="sub">{{ t('share.inheritedFrom', { page: u.page_title || t('common.untitled') }) }}</div></div>
            <span class="rl">{{ t('share.roles.' + u.role) }}</span>
          </div>
        </div>
        <div v-if="!manage" class="note"><Icon name="lock" :size="14" />{{ t('share.noManage') }}</div>
      </template>
      <template v-else>
        <div v-if="!data.public_enabled" class="note"><Icon name="lock" :size="14" />{{ t('share.disabledByAdmin') }}</div>
        <template v-else-if="!data.public">
          <div class="pub-empty">
            <Icon name="globe" :size="28" class="globe" />
            <div class="pe-t">{{ t('share.publishTitle') }}<InfoTip :text="t('share.publishTip')" :width="280" /></div>
            <Btn v-if="manage" variant="primary" :loading="busy" @click="publish({})">{{ t('share.publishBtn') }}</Btn>
          </div>
        </template>
        <template v-else>
          <div class="linkrow">
            <span class="live" :class="{ off: data.public.expired }"><span class="dot" />{{ data.public.expired ? t('share.expired') : t('share.live') }}</span>
            <input v-if="manage" class="link" :value="link" readonly @focus="($event.target as HTMLInputElement).select()" />
            <Btn v-if="manage" size="sm" icon="copy" @click="copy">{{ t('share.copyLink') }}</Btn>
            <a v-if="manage" :href="link" target="_blank" rel="noopener" class="ext"><IconBtn icon="external-link" :size="15" :label="t('common.open')" /></a>
          </div>
          <div class="opts">
            <div class="opt-row"><span class="ol">{{ t('share.includeChildren') }}<InfoTip :text="t('share.includeChildrenTip')" /></span><Switch :model-value="data.public.include_children" :disabled="!manage || busy" @update:model-value="(v: boolean) => publish({ include_children: v })" /></div>
            <div class="opt-row">
              <span class="ol">{{ t('share.password') }}<InfoTip :text="t('share.passwordTip')" /></span>
              <template v-if="pwEditing">
                <input v-model="pw" class="pw" type="password" autocomplete="new-password" :placeholder="t('share.passwordPh')" @keydown.enter="savePassword" />
                <Btn size="sm" variant="primary" :loading="busy" @click="savePassword">{{ t('common.save') }}</Btn>
                <Btn size="sm" variant="ghost" @click="pwEditing = false; pw = ''">{{ t('common.cancel') }}</Btn>
              </template>
              <template v-else>
                <span class="val">{{ data.public.has_password ? t('share.passwordSet') : t('share.passwordNone') }}</span>
                <Btn v-if="manage" size="sm" variant="ghost" @click="pwEditing = true">{{ data.public.has_password ? t('share.changePassword') : t('share.setPassword') }}</Btn>
                <Btn v-if="manage && data.public.has_password" size="sm" variant="ghost" @click="publish({ password: '' })">{{ t('common.remove') }}</Btn>
              </template>
            </div>
            <div class="opt-row">
              <span class="ol">{{ t('share.expiry') }}</span>
              <select v-model="expiry" class="sel" :disabled="!manage || busy">
                <option value="never">{{ t('share.never') }}</option>
                <option v-if="data.public.expires_at" value="custom">{{ formatDate(data.public.expires_at, true) }}</option>
                <option value="1">{{ t('share.days', { n: 1 }) }}</option>
                <option value="7">{{ t('share.days', { n: 7 }) }}</option>
                <option value="30">{{ t('share.days', { n: 30 }) }}</option>
              </select>
            </div>
            <div class="opt-row"><span class="ol">{{ t('share.allowIndex') }}<InfoTip :text="t('share.allowIndexTip')" /></span><Switch :model-value="data.public.allow_index" :disabled="!manage || busy" @update:model-value="(v: boolean) => publish({ allow_index: v })" /></div>
          </div>
          <div v-if="manage" class="acts">
            <Btn size="sm" variant="ghost" icon="refresh" @click="rotate">{{ t('share.rotate') }}</Btn>
            <span class="ln-spacer" />
            <Btn size="sm" variant="danger" @click="unpublish">{{ t('share.unpublish') }}</Btn>
          </div>
        </template>
        <div v-for="p in data.inherited_public" :key="p.id" class="note"><Icon name="globe" :size="14" />{{ t('share.inheritedPublic', { page: p.page_title || t('common.untitled') }) }}</div>
      </template>
    </div>
  </Modal>
</template>

<style scoped>
.sh { display: flex; flex-direction: column; gap: 14px; min-height: 220px; }
.ld { height: 120px; }
.inv { display: flex; gap: 8px; align-items: center; }
.pick { flex: 1; position: relative; min-width: 0; }
.q { width: 100%; height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); color: var(--text); }
.q:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.chip { display: inline-flex; align-items: center; gap: 6px; height: 32px; padding: 0 6px 0 4px; border-radius: var(--radius-m); background: var(--bg-hover); max-width: 100%; }
.x { border: 0; background: none; color: var(--text-3); display: flex; padding: 2px; border-radius: 4px; }
.x:hover { background: var(--bg-active); color: var(--text); }
.sugg { position: absolute; top: 36px; left: 0; right: 0; background: var(--bg-elev); border: 1px solid var(--border); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 4px; z-index: 5; }
.opt { display: flex; align-items: center; gap: 8px; width: 100%; border: 0; background: none; padding: 6px 8px; border-radius: 5px; text-align: left; color: var(--text); }
.opt:hover { background: var(--bg-hover); }
.un { color: var(--text-3); font-size: 12.5px; }
.sel { height: 32px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); color: var(--text); }
.sel.sm { height: 28px; font-size: 13px; }
.list { display: flex; flex-direction: column; gap: 2px; }
.row { display: flex; align-items: center; gap: 10px; padding: 6px 4px; border-radius: var(--radius-m); }
.row.inh { opacity: .85; }
.av { width: 28px; height: 28px; border-radius: 50%; background: var(--accent-soft); color: var(--accent); display: flex; align-items: center; justify-content: center; font-size: 13px; font-weight: 600; flex: none; }
.av.sm { width: 22px; height: 22px; font-size: 11px; }
.av.sp { background: var(--bg-hover); color: var(--text-2); }
.who { flex: 1; min-width: 0; }
.nm { font-size: 13.5px; display: flex; align-items: center; gap: 6px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.sub { font-size: 12px; color: var(--text-3); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.you { font-size: 11.5px; color: var(--text-3); }
.rl { font-size: 13px; color: var(--text-2); white-space: nowrap; }
.note { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-2); background: var(--bg-soft); border-radius: var(--radius-m); padding: 8px 10px; }
.pub-empty { display: flex; flex-direction: column; align-items: center; gap: 12px; padding: 24px 0 12px; }
.globe { color: var(--text-3); }
.pe-t { display: flex; align-items: center; gap: 4px; font-weight: 500; }
.linkrow { display: flex; align-items: center; gap: 8px; }
.live { display: inline-flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--success); white-space: nowrap; }
.live .dot { width: 7px; height: 7px; border-radius: 50%; background: currentColor; }
.live.off { color: var(--text-3); }
.link { flex: 1; min-width: 0; height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg-soft); color: var(--text-2); font-family: var(--font-mono); font-size: 12px; }
.ext { display: flex; }
.opts { display: flex; flex-direction: column; border: 1px solid var(--border); border-radius: var(--radius-m); }
.opt-row { display: flex; align-items: center; gap: 8px; min-height: 44px; padding: 6px 12px; }
.opt-row + .opt-row { border-top: 1px solid var(--border); }
.ol { flex: 1; display: flex; align-items: center; gap: 4px; font-size: 13.5px; }
.val { font-size: 13px; color: var(--text-2); }
.pw { height: 28px; width: 160px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); color: var(--text); }
.acts { display: flex; align-items: center; }
@media (max-width: 760px) {
  .inv { flex-wrap: wrap; }
  .pick { flex-basis: 100%; }
}
</style>
