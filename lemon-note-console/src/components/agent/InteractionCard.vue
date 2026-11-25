<script setup lang="ts">
// 审批卡片与提问卡片。
import { ref, computed, reactive } from 'vue'
import { useI18n } from 'vue-i18n'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import type { Interaction } from '@/stores/agent'
import { useAgent } from '@/stores/agent'
import { toolLabel } from './toolLabel'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const props = defineProps<{ it: Interaction }>()
const { t } = useI18n()
const agent = useAgent()
const toast = useToast()
const reason = ref('')
const busy = ref(false)
const picks = reactive<Record<number, string[]>>({})
const others = reactive<Record<number, string>>({})
const qs = computed(() => props.it.questions || [])

async function approve(ok: boolean, always = false) {
  busy.value = true
  try { await agent.respond(props.it.id, { approve: ok, always, answer: reason.value }) } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

function toggle(qi: number, label: string, multi: boolean) {
  const cur = picks[qi] || []
  if (multi) picks[qi] = cur.includes(label) ? cur.filter((x) => x !== label) : [...cur, label]
  else picks[qi] = cur[0] === label ? [] : [label]
}

const canSubmit = computed(() => qs.value.every((_: any, i: number) => (picks[i]?.length || 0) > 0 || (others[i] || '').trim()))

async function answer() {
  const lines = qs.value.map((q: any, i: number) => {
    const a = [...(picks[i] || [])]
    if ((others[i] || '').trim()) a.push(others[i].trim())
    return `${q.header || q.question}：${a.join('、')}`
  })
  busy.value = true
  try { await agent.respond(props.it.id, { answer: lines.join('\n') }) } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <div class="ic" :class="it.kind">
    <template v-if="it.kind === 'approval'">
      <div class="ic-title"><Icon :name="it.destructive ? 'warning' : 'shield'" :size="15" />{{ t('agent.approveTitle') }}</div>
      <div class="ic-desc">{{ toolLabel(it.name || '', it.args, it.title || it.name || '') }}</div>
      <input v-model="reason" class="ic-input" :placeholder="t('agent.denyReason')" />
      <div class="ic-actions">
        <Btn size="sm" :disabled="busy" @click="approve(false)">{{ t('agent.deny') }}</Btn>
        <span class="ln-spacer" />
        <Btn size="sm" variant="ghost" :disabled="busy" @click="approve(true, true)">{{ t('agent.alwaysAllow') }}</Btn>
        <Btn size="sm" :variant="it.destructive ? 'danger' : 'primary'" :disabled="busy" @click="approve(true)">{{ t('agent.allow') }}</Btn>
      </div>
    </template>
    <template v-else>
      <div v-for="(q, qi) in qs" :key="qi" class="q">
        <div class="q-text">{{ q.question }}</div>
        <div class="opts">
          <button v-for="o in q.options" :key="o.label" class="opt" :class="{ on: (picks[qi] || []).includes(o.label) }" @click="toggle(qi, o.label, !!q.multi)">
            <span class="opt-l">{{ o.label }}</span>
            <span v-if="o.description" class="opt-d">{{ o.description }}</span>
          </button>
        </div>
        <input v-model="others[qi]" class="ic-input" :placeholder="t('agent.otherAnswer')" />
      </div>
      <div class="ic-actions"><span class="ln-spacer" /><Btn size="sm" variant="primary" :disabled="!canSubmit || busy" @click="answer">{{ t('agent.submit') }}</Btn></div>
    </template>
  </div>
</template>

<style scoped>
.ic { border: 1px solid var(--border-strong); border-radius: var(--radius-l); padding: 12px; background: var(--bg-elev); display: flex; flex-direction: column; gap: 10px; box-shadow: var(--shadow-soft); }
.ic.approval { border-color: var(--accent); }
.ic-title { display: flex; align-items: center; gap: 6px; font-weight: 600; font-size: 13.5px; }
.ic-desc { font-size: 13px; color: var(--text-2); }
.ic-input { height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; font-size: 13px; }
.ic-input:focus { border-color: var(--accent); }
.ic-actions { display: flex; gap: 6px; align-items: center; }
.q { display: flex; flex-direction: column; gap: 6px; }
.q-text { font-weight: 500; font-size: 13.5px; }
.opts { display: flex; flex-direction: column; gap: 4px; }
.opt { display: flex; flex-direction: column; align-items: flex-start; gap: 1px; text-align: left; border: 1px solid var(--border); background: var(--bg); border-radius: var(--radius-m); padding: 7px 10px; }
.opt:hover { border-color: var(--border-strong); }
.opt.on { border-color: var(--accent); background: var(--accent-soft); }
.opt-l { font-size: 13px; font-weight: 500; }
.opt-d { font-size: 12px; color: var(--text-2); }
</style>
