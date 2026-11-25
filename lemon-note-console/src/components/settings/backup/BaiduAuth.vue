<script setup lang="ts">
// 百度网盘设备码授权：显示用户码与二维码，按服务器给的间隔轮询。
import { ref, watch, onBeforeUnmount } from 'vue'
import { useI18n } from 'vue-i18n'
import QRCode from 'qrcode'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { errorText } from '@/i18n'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ targetId: string }>()
const emit = defineEmits<{ authorized: [] }>()
const { t } = useI18n()
const dev = ref<any>(null)
const qr = ref('')
const err = ref('')
const ok = ref(false)
let timer: number | undefined
let interval = 5

async function start() {
  dev.value = null; err.value = ''; ok.value = false; qr.value = ''
  try {
    dev.value = await api.post(`/api/admin/backup/targets/${props.targetId}/baidu/device`)
    interval = Math.max(3, dev.value.interval || 5)
    const link = dev.value.verification_url + (dev.value.verification_url.includes('?') ? '&' : '?') + 'code=' + encodeURIComponent(dev.value.user_code)
    qr.value = await QRCode.toDataURL(link, { margin: 1, width: 180 })
    timer = window.setTimeout(poll, interval * 1000)
  } catch (e) { err.value = errorText(e) }
}
async function poll() {
  try {
    const r = await api.post(`/api/admin/backup/targets/${props.targetId}/baidu/poll`)
    if (r.status === 'authorized') { ok.value = true; emit('authorized'); return }
    if (r.slow_down) interval += 5
    if (open.value) timer = window.setTimeout(poll, interval * 1000)
  } catch (e) { err.value = errorText(e) }
}
watch(open, (v) => { clearTimeout(timer); if (v) start() }, { immediate: true })
onBeforeUnmount(() => clearTimeout(timer))
</script>

<template>
  <Modal v-model:open="open" :title="t('backup.baiduAuth')" :width="420">
    <div class="ba">
      <div v-if="err" class="err">{{ err }}</div>
      <div v-else-if="ok" class="ok"><Icon name="check" :size="20" />{{ t('backup.baiduOk') }}</div>
      <template v-else-if="dev">
        <img v-if="qr" :src="qr" alt="" class="qr" />
        <div class="code">{{ dev.user_code }}</div>
        <p class="tip">{{ t('backup.baiduTip') }}</p>
        <a :href="dev.verification_url" target="_blank" rel="noopener" class="link">{{ dev.verification_url }}</a>
        <div class="wait"><span class="spin" />{{ t('backup.baiduWaiting') }}</div>
      </template>
      <div v-else class="wait"><span class="spin" /></div>
    </div>
    <template #footer><Btn @click="open = false">{{ ok ? t('common.done') : t('common.close') }}</Btn></template>
  </Modal>
</template>

<style scoped>
.ba { display: flex; flex-direction: column; align-items: center; gap: 10px; padding: 6px 0; text-align: center; }
.qr { width: 180px; height: 180px; border-radius: 8px; background: #fff; padding: 6px; }
.code { font-family: var(--font-mono); font-size: 22px; letter-spacing: 3px; font-weight: 600; }
.tip { margin: 0; color: var(--text-2); font-size: 13px; }
.link { font-size: 12.5px; color: var(--accent); word-break: break-all; }
.wait { display: flex; align-items: center; gap: 8px; color: var(--text-3); font-size: 12.5px; }
.spin { width: 14px; height: 14px; border: 2px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: sp .8s linear infinite; }
@keyframes sp { to { transform: rotate(360deg); } }
.err { color: var(--danger); font-size: 13px; }
.ok { color: var(--success); display: flex; align-items: center; gap: 6px; font-weight: 500; }
</style>
