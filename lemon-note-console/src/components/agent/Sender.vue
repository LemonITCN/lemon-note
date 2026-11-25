<script setup lang="ts">
import { ref, computed, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import Menu from '@/components/ui/Menu.vue'
import RefChip from './RefChip.vue'
import ContextRing from './ContextRing.vue'
import { useAgent } from '@/stores/agent'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { REF_MIME } from '@/lib/dnd'
import { uploadFile } from '@/lib/files'

const { t } = useI18n()
const agent = useAgent()
const toast = useToast()
const route = useRoute()
const text = ref('')
const ta = ref<HTMLTextAreaElement>()
const dropping = ref(false)
const images = ref<{ id: string; name: string }[]>([])
const busy = ref(false)

const modeItems = computed(() => [
  { value: 'plan', label: t('agent.modePlan'), icon: 'eye' },
  { value: 'ask_all', label: t('agent.modeAskAll'), icon: 'shield' },
  { value: 'ask_destructive', label: t('agent.modeAskDestructive'), icon: 'warning' },
  { value: 'auto', label: t('agent.modeAuto'), icon: 'sparkle' },
])
const modeLabel = computed(() => modeItems.value.find((m) => m.value === agent.settings.mode)?.label || '')
const modeMenu = computed(() => modeItems.value.map((m) => ({ label: m.label, icon: m.icon, checked: agent.settings.mode === m.value, run: () => { agent.settings.mode = m.value; agent.saveSettings() } })))
const efforts = computed(() => (agent.currentModel?.efforts || []).filter((e) => e !== 'default'))
const effortName = (e: string) => ({ none: t('agent.effortNone'), minimal: t('agent.effortMin'), low: t('agent.effortLow'), medium: t('agent.effortMid'), high: t('agent.effortHigh'), xhigh: t('agent.effortXHigh'), max: t('agent.effortMax') } as Record<string, string>)[e] || e
const modelMenu = computed(() => {
  const items: any[] = agent.models.map((m) => ({ label: m.name, checked: (agent.settings.model || agent.defaultModel) === m.id, hint: m.vision ? '图' : '', run: () => { agent.settings.model = m.id; agent.saveSettings() } }))
  if (efforts.value.length) {
    items.unshift({ heading: t('agent.effort') }, ...efforts.value.map((e) => ({ label: effortName(e), checked: agent.settings.effort === e, run: () => { agent.settings.effort = e; agent.saveSettings() } })), { separator: true }, { heading: t('agent.model') })
  }
  return items
})
const modelShort = computed(() => {
  const m = agent.currentModel
  const e = efforts.value.length ? ' · ' + effortName(agent.settings.effort || 'high') : ''
  return (m?.name || agent.settings.model || '…') + e
})

function autosize() {
  const el = ta.value
  if (!el) return
  el.style.height = 'auto'
  el.style.height = Math.min(220, el.scrollHeight) + 'px'
}

async function submit() {
  const v = text.value.trim()
  if ((!v && !agent.draftRefs.length && !images.value.length) || busy.value) return
  busy.value = true
  try {
    await agent.send(v, route.name === 'page' ? String(route.params.id) : '', images.value.map((i) => i.id))
    text.value = ''
    images.value = []
    await nextTick()
    autosize()
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Enter' && !e.shiftKey && !e.isComposing) {
    e.preventDefault()
    submit()
  }
}

function onDrop(e: DragEvent) {
  dropping.value = false
  const raw = e.dataTransfer?.getData(REF_MIME)
  if (raw) {
    e.preventDefault()
    try {
      const r = JSON.parse(raw)
      agent.addRef({ kind: r.kind === 'database' ? 'database' : 'page', id: r.id, title: r.title, icon: r.icon })
    } catch {}
    return
  }
  const files = Array.from(e.dataTransfer?.files || []).filter((f) => f.type.startsWith('image/'))
  if (files.length) { e.preventDefault(); addImages(files) }
}

async function addImages(files: File[]) {
  for (const f of files.slice(0, 4)) {
    try { const r = await uploadFile(f); images.value.push({ id: r.id, name: r.name }) } catch (e) { toast.error(errorText(e)) }
  }
}

function onPaste(e: ClipboardEvent) {
  const files = Array.from(e.clipboardData?.files || []).filter((f) => f.type.startsWith('image/'))
  if (files.length) { e.preventDefault(); addImages(files) }
}

defineExpose({ focus: () => ta.value?.focus() })
</script>

<template>
  <div class="sender" :class="{ dropping }" @dragover.prevent="dropping = true" @dragleave="dropping = false" @drop="onDrop">
    <div v-if="agent.draftRefs.length || images.length" class="chips">
      <RefChip v-for="(r, i) in agent.draftRefs" :key="i" :r="r" removable @remove="agent.draftRefs.splice(i, 1)" />
      <span v-for="(im, i) in images" :key="im.id" class="imgchip"><img :src="'/api/files/' + im.id" alt="" /><button @click="images.splice(i, 1)"><Icon name="close" :size="10" /></button></span>
    </div>
    <textarea ref="ta" v-model="text" rows="2" :placeholder="t('agent.placeholder')" @input="autosize" @keydown="onKey" @paste="onPaste" />
    <div class="bar">
      <Menu :items="modeMenu" side="top"><button class="pill"><Icon name="shield" :size="13" />{{ modeLabel }}<Icon name="chevron-down" :size="11" /></button></Menu>
      <span class="ln-spacer" />
      <ContextRing :pct="agent.contextPct" :title="t('agent.contextTip', { used: agent.context, budget: agent.budget })" />
      <Menu :items="modelMenu" side="top" align="end" :width="240"><button class="pill model ln-ellipsis">{{ modelShort }}<Icon name="chevron-down" :size="11" /></button></Menu>
      <button v-if="agent.running" class="send stop" :title="t('agent.stop')" @click="agent.stop()"><Icon name="stop" :size="14" /></button>
      <button v-else class="send" :disabled="!text.trim() && !agent.draftRefs.length && !images.length" :title="t('agent.send')" @click="submit"><Icon name="send" :size="15" /></button>
    </div>
  </div>
</template>

<style scoped>
.sender { margin: 0 10px 10px; border: 1px solid var(--border-strong); border-radius: 12px; background: var(--bg-elev); padding: 8px 8px 6px; display: flex; flex-direction: column; gap: 6px; box-shadow: var(--shadow-soft); transition: border-color .12s; }
.sender:focus-within { border-color: var(--accent); }
.sender.dropping { border-color: var(--accent); background: var(--accent-soft); }
.chips { display: flex; flex-wrap: wrap; gap: 4px; }
.imgchip { position: relative; width: 40px; height: 40px; border-radius: 5px; overflow: hidden; border: 1px solid var(--border); }
.imgchip img { width: 100%; height: 100%; object-fit: cover; }
.imgchip button { position: absolute; top: 1px; right: 1px; border: 0; background: rgba(0,0,0,.6); color: #fff; border-radius: 3px; padding: 1px; display: flex; }
textarea { border: 0; outline: none; resize: none; background: none; font-size: 14px; line-height: 1.55; padding: 2px 4px; max-height: 220px; color: var(--text); }
.bar { display: flex; align-items: center; gap: 6px; }
.pill { display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--border); background: var(--bg); height: 24px; padding: 0 8px; border-radius: 12px; color: var(--text-2); font-size: 12px; max-width: 170px; }
.pill:hover { color: var(--text); border-color: var(--border-strong); }
.send { width: 28px; height: 28px; border-radius: 50%; border: 0; background: var(--accent); color: #fff; display: flex; align-items: center; justify-content: center; flex: none; }
.send:disabled { opacity: .35; }
.send.stop { background: var(--text); color: var(--bg); }
</style>
