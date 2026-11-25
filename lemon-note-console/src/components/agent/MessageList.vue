<script setup lang="ts">
import { computed, ref, watch, nextTick } from 'vue'
import { useI18n } from 'vue-i18n'
import Markdown from './Markdown.vue'
import ToolRow from './ToolRow.vue'
import RefChip from './RefChip.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Message, Part } from '@/stores/agent'
import { useAgent } from '@/stores/agent'

const { t } = useI18n()
const agent = useAgent()
const scroller = ref<HTMLDivElement>()
const openReasoning = ref(new Set<string>())
const stick = ref(true)

// 工具结果按 call_id 挂到工具调用上
const results = computed(() => {
  const m = new Map<string, Part>()
  for (const msg of agent.messages) if (msg.role === 'tool') for (const p of msg.parts) if (p.call_id) m.set(p.call_id, p)
  return m
})
const shown = computed(() => agent.messages.filter((m) => m.role !== 'tool'))

function resultOf(p: Part) {
  const r = results.value.get(p.call_id || '')
  const live = agent.toolStatus.get(p.call_id || '')
  if (r) return { status: r.status || (r.is_error ? 'error' : 'done'), text: r.text, duration: r.duration, data: r.data }
  if (live) return { status: live.status, text: live.text, duration: live.duration, data: live.data }
  return { status: agent.running ? 'running' : 'interrupted', text: '' }
}

function errOf(m: Message) {
  const p = m.parts.find((x) => x.status === 'error' && x.data?.error)
  return p ? p.data.error : ''
}

function onScroll() {
  const el = scroller.value
  if (el) stick.value = el.scrollHeight - el.scrollTop - el.clientHeight < 80
}

async function toBottom(force = false) {
  await nextTick()
  const el = scroller.value
  if (el && (stick.value || force)) el.scrollTop = el.scrollHeight
}

watch(() => [agent.messages.length, agent.live.text.length, agent.live.reasoning.length, agent.live.tools.length, agent.interactions.length], () => toBottom())
watch(() => agent.currentId, () => toBottom(true))
defineExpose({ toBottom })

function toggleReasoning(id: string) {
  const s = new Set(openReasoning.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  openReasoning.value = s
}
</script>

<template>
  <div ref="scroller" class="ml" @scroll="onScroll">
    <div v-for="m in shown" :key="m.id" class="msg" :class="m.role">
      <template v-if="m.role === 'user'">
        <div class="bubble">
          <div v-if="m.parts[0]?.refs?.length" class="refs"><RefChip v-for="(r, i) in m.parts[0].refs" :key="i" :r="r" /></div>
          <div v-if="m.parts[0]?.text" class="utext">{{ m.parts[0].text }}</div>
          <div v-for="(p, i) in m.parts.filter((x) => x.type === 'image')" :key="'img' + i" class="uimg"><img :src="'/api/files/' + p.data?.file_id" alt="" /></div>
        </div>
      </template>
      <template v-else>
        <template v-for="(p, i) in m.parts" :key="i">
          <div v-if="p.type === 'reasoning' && p.text" class="reason">
            <button class="reason-head" @click="toggleReasoning(m.id + i)"><Icon name="sparkle" :size="13" />{{ t('agent.thinking') }}<Icon :name="openReasoning.has(m.id + i) ? 'chevron-up' : 'chevron-down'" :size="12" /></button>
            <div v-if="openReasoning.has(m.id + i)" class="reason-body">{{ p.text }}</div>
          </div>
          <Markdown v-else-if="p.type === 'text' && p.text" :text="p.text" class="atext" />
          <ToolRow v-else-if="p.type === 'tool_call'" :name="p.name || ''" :title="p.title || p.name || ''" :args="p.args" v-bind="resultOf(p)" />
        </template>
        <div v-if="errOf(m)" class="err"><Icon name="warning" :size="14" />{{ errOf(m) }}</div>
      </template>
    </div>

    <div v-if="agent.live.active || agent.live.compacting || agent.live.sub || (agent.running && !agent.live.active)" class="msg assistant live">
      <div v-if="agent.live.reasoning && !agent.live.text" class="reason thinking"><Icon name="sparkle" :size="13" />{{ t('agent.thinking') }}<span class="dots" /></div>
      <Markdown v-if="agent.live.text" :text="agent.live.text" class="atext" />
      <ToolRow v-for="tl in agent.live.tools" :key="tl.call_id" :name="tl.name" :title="tl.title" :args="tl.args" :status="agent.toolStatus.get(tl.call_id)?.status || tl.status"
        :text="agent.toolStatus.get(tl.call_id)?.text" :duration="agent.toolStatus.get(tl.call_id)?.duration" />
      <div v-if="agent.live.compacting" class="hint"><span class="spin" />{{ t('agent.compacting') }}</div>
      <div v-if="agent.live.sub" class="hint"><span class="spin" />{{ agent.live.sub }}</div>
      <div v-if="agent.live.retry" class="hint warn">{{ agent.live.retry }}</div>
      <div v-if="agent.running && !agent.live.text && !agent.live.reasoning && !agent.live.tools.length && !agent.interactions.length" class="hint"><span class="spin" />{{ t('agent.working') }}</div>
    </div>
    <div v-if="agent.live.notice" class="notice">{{ agent.live.notice }}</div>
    <div v-if="agent.error" class="err"><Icon name="warning" :size="14" />{{ agent.error }}</div>
    <slot name="tail" />
  </div>
</template>

<style scoped>
.ml { flex: 1; overflow-y: auto; padding: 14px 14px 8px; display: flex; flex-direction: column; gap: 14px; }
.msg.user { display: flex; justify-content: flex-end; }
.bubble { max-width: 88%; background: var(--accent-soft); border-radius: 12px 12px 4px 12px; padding: 8px 12px; display: flex; flex-direction: column; gap: 6px; }
.utext { white-space: pre-wrap; word-break: break-word; line-height: 1.6; }
.refs { display: flex; flex-wrap: wrap; gap: 4px; }
.uimg img { max-width: 180px; max-height: 140px; border-radius: 6px; display: block; }
.msg.assistant { display: flex; flex-direction: column; gap: 4px; }
.atext { font-size: 14px; }
.reason { font-size: 12.5px; color: var(--text-3); }
.reason-head { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-3); font-size: 12.5px; padding: 2px 4px; border-radius: 4px; }
.reason-head:hover { background: var(--bg-hover); color: var(--text-2); }
.reason-body { white-space: pre-wrap; border-left: 2px solid var(--border-strong); padding: 2px 0 2px 10px; margin: 2px 0 4px 6px; max-height: 300px; overflow: auto; line-height: 1.55; }
.reason.thinking { display: inline-flex; align-items: center; gap: 4px; }
.dots::after { content: '…'; animation: blink 1.2s infinite; }
@keyframes blink { 50% { opacity: .2; } }
.hint { display: flex; align-items: center; gap: 6px; font-size: 12.5px; color: var(--text-3); }
.hint.warn { color: var(--warning); }
.spin { width: 11px; height: 11px; border-radius: 50%; border: 1.6px solid var(--accent); border-right-color: transparent; animation: sp .7s linear infinite; flex: none; }
@keyframes sp { to { transform: rotate(360deg); } }
.err { display: flex; gap: 6px; align-items: flex-start; color: var(--danger); font-size: 13px; background: var(--danger-bg); border-radius: var(--radius-m); padding: 8px 10px; }
.notice { font-size: 12.5px; color: var(--text-3); text-align: center; }
</style>
