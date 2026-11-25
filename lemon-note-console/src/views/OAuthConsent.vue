<script setup lang="ts">
// MCP 客户端（Claude Code、Cursor 等）的 OAuth 授权确认页。
import { ref, computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import AuthFrame from './AuthFrame.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { errorText } from '@/i18n'

const { t } = useI18n()
const route = useRoute()
const session = useSession()
const info = ref<any>(null)
const err = ref('')
const write = ref(true)
const allSpaces = ref(true)
const picked = ref<string[]>([])
const spaces = ref<{ id: string; name: string }[]>([])
const busy = ref(false)
const done = ref(false)

const params = computed(() => {
  const q = route.query
  const g = (k: string) => (typeof q[k] === 'string' ? (q[k] as string) : '')
  return { response_type: g('response_type'), client_id: g('client_id'), redirect_uri: g('redirect_uri'), state: g('state'), scope: g('scope'),
    code_challenge: g('code_challenge'), code_challenge_method: g('code_challenge_method'), resource: g('resource') }
})

onMounted(async () => {
  try {
    info.value = await api.post('/api/oauth/request', params.value)
    write.value = !!info.value.write
    if (!info.value.ok && !info.value.redirect_host) err.value = info.value.error_description || t('oauth.invalid')
    const r = await api.get('/api/spaces')
    spaces.value = (r.spaces || []).map((x: any) => ({ id: x.space.id, name: x.space.name }))
  } catch (e) { err.value = errorText(e) }
})

async function decide(approve: boolean) {
  busy.value = true
  try {
    const r = await api.post('/api/oauth/decide', { ...params.value, approve, write: write.value, spaces: allSpaces.value ? [] : picked.value })
    done.value = true
    window.location.href = r.redirect
  } catch (e) { err.value = errorText(e); busy.value = false }
}
</script>

<template>
  <AuthFrame :title="t('oauth.title')">
    <div v-if="err" class="err"><Icon name="warning" :size="16" />{{ err }}</div>
    <div v-else-if="done" class="done">{{ t('oauth.done') }}</div>
    <template v-else-if="info">
      <div class="app">
        <span class="ai"><Icon name="plug" :size="20" /></span>
        <div class="an"><div class="nm">{{ info.client_name }}</div><div class="host">{{ info.redirect_host }}</div></div>
      </div>
      <p class="lead">{{ t('oauth.lead', { app: info.client_name, user: session.user?.display_name }) }}</p>
      <div class="opts">
        <div class="row"><span>{{ t('oauth.read') }}</span><Icon name="check" :size="16" class="ok" /></div>
        <div class="row"><span class="lbl">{{ t('oauth.write') }}<InfoTip :text="t('oauth.writeTip')" /></span><Switch v-model="write" /></div>
        <div class="row"><span class="lbl">{{ t('oauth.allSpaces') }}<InfoTip :text="t('oauth.spacesTip')" /></span><Switch v-model="allSpaces" /></div>
        <div v-if="!allSpaces" class="spaces">
          <label v-for="s in spaces" :key="s.id" class="chk"><input v-model="picked" type="checkbox" :value="s.id" />{{ s.name }}</label>
        </div>
      </div>
      <div class="acts">
        <Btn :disabled="busy" @click="decide(false)">{{ t('oauth.deny') }}</Btn>
        <Btn variant="primary" :loading="busy" :disabled="!allSpaces && !picked.length" @click="decide(true)">{{ t('oauth.allow') }}</Btn>
      </div>
    </template>
  </AuthFrame>
</template>

<style scoped>
.err { display: flex; gap: 8px; align-items: flex-start; color: var(--danger); font-size: 13.5px; }
.done { color: var(--text-2); font-size: 13.5px; }
.app { display: flex; align-items: center; gap: 12px; padding: 12px; background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); }
.ai { width: 38px; height: 38px; border-radius: 9px; background: var(--accent-soft); color: var(--accent); display: flex; align-items: center; justify-content: center; flex: none; }
.nm { font-weight: 600; }
.host { font-size: 12px; color: var(--text-3); font-family: var(--font-mono); word-break: break-all; }
.lead { color: var(--text-2); font-size: 13.5px; line-height: 1.6; margin: 14px 0; }
.opts { border: 1px solid var(--border); border-radius: var(--radius-m); }
.row { display: flex; align-items: center; justify-content: space-between; min-height: 42px; padding: 0 12px; font-size: 13.5px; }
.row + .row { border-top: 1px solid var(--border); }
.lbl { display: flex; align-items: center; gap: 4px; }
.ok { color: var(--success); }
.spaces { border-top: 1px solid var(--border); padding: 8px 12px; display: flex; flex-direction: column; gap: 6px; }
.chk { display: flex; align-items: center; gap: 8px; font-size: 13.5px; }
.acts { display: flex; justify-content: flex-end; gap: 8px; margin-top: 18px; }
</style>
