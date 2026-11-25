<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import Btn from '@/components/ui/Btn.vue'
import MessageList from './MessageList.vue'
import Sender from './Sender.vue'
import InteractionCard from './InteractionCard.vue'
import ConversationList from './ConversationList.vue'
import { useAgent } from '@/stores/agent'
import { useUI } from '@/stores/ui'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const { t } = useI18n()
const agent = useAgent()
const ui = useUI()
const s = useSession()
const toast = useToast()
const listOpen = ref(false)
const planOpen = ref(true)
const doneCount = computed(() => agent.todos.filter((x) => x.status === 'completed').length)

onMounted(async () => {
  await agent.loadModels()
  agent.loadList().catch(() => {})
  if (agent.currentId) agent.open(agent.currentId).catch(() => agent.newConversation())
})

async function revert() {
  try {
    const n = await agent.revert()
    toast.success(t('agent.reverted', { n }))
  } catch (e) { toast.error(errorText(e)) }
}

function pick(id: string) {
  listOpen.value = false
  agent.open(id).catch((e) => toast.error(errorText(e)))
}
</script>

<template>
  <div class="agent">
    <div class="head">
      <IconBtn icon="menu" :label="t('agent.history')" :active="listOpen" @click="listOpen = !listOpen" />
      <span class="title ln-ellipsis">{{ agent.title || t('agent.newChat') }}</span>
      <IconBtn icon="plus" :label="t('agent.newChat')" @click="agent.newConversation(); listOpen = false" />
      <IconBtn icon="chevrons-left" :label="t('common.close')" @click="ui.toggleAgent(false)" />
    </div>
    <ConversationList v-if="listOpen" @pick="pick" />
    <template v-else>
      <div v-if="!agent.ready" class="empty">
        <Icon name="assistant" :size="28" />
        <p>{{ agent.providerError || t('agent.noProvider') }}</p>
        <Btn v-if="s.user?.role === 'admin'" size="sm" @click="ui.openSettings('ai')">{{ t('settings.ai') }}</Btn>
      </div>
      <template v-else>
        <MessageList>
          <template #tail>
            <div v-if="!agent.messages.length && !agent.running" class="welcome">
              <Icon name="assistant" :size="26" />
              <p>{{ t('agent.welcome') }}</p>
            </div>
            <div v-if="agent.lastCheckpoint && !agent.running" class="revert">
              <span>{{ t('agent.changedPages', { n: agent.lastCheckpoint.pages }) }}</span>
              <button @click="revert"><Icon name="undo" :size="13" />{{ t('agent.revert') }}</button>
            </div>
          </template>
        </MessageList>
        <div v-if="agent.interactions.length" class="inter">
          <InteractionCard v-for="it in agent.interactions" :key="it.id" :it="it" />
        </div>
        <div v-if="agent.todos.length" class="plan">
          <button class="plan-head" @click="planOpen = !planOpen"><Icon name="list-check" :size="14" />{{ t('agent.plan') }} {{ doneCount }}/{{ agent.todos.length }}<span class="ln-spacer" /><Icon :name="planOpen ? 'chevron-down' : 'chevron-up'" :size="12" /></button>
          <div v-if="planOpen" class="plan-body">
            <div v-for="(td, i) in agent.todos" :key="i" class="todo" :class="td.status">
              <span class="box"><Icon v-if="td.status === 'completed'" name="check" :size="11" :stroke="2.4" /><span v-else-if="td.status === 'in_progress'" class="dot" /></span>
              <span>{{ td.content }}</span>
            </div>
          </div>
        </div>
        <Sender />
      </template>
    </template>
  </div>
</template>

<style scoped>
.agent { height: 100%; display: flex; flex-direction: column; background: var(--bg); min-width: 0; }
.head { display: flex; align-items: center; gap: 4px; height: 44px; padding: 0 6px 0 8px; border-bottom: 1px solid var(--border); flex: none; }
.title { flex: 1; font-weight: 600; font-size: 13.5px; padding: 0 4px; }
.empty, .welcome { display: flex; flex-direction: column; align-items: center; gap: 10px; color: var(--text-3); text-align: center; padding: 40px 24px; }
.empty { flex: 1; justify-content: center; }
.welcome p, .empty p { margin: 0; max-width: 280px; line-height: 1.6; font-size: 13px; }
.revert { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 12.5px; color: var(--text-3); border-top: 1px dashed var(--border-strong); padding-top: 8px; }
.revert button { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12.5px; padding: 3px 6px; border-radius: 4px; }
.revert button:hover { background: var(--bg-hover); color: var(--text); }
.inter { padding: 0 10px 8px; display: flex; flex-direction: column; gap: 8px; }
.plan { margin: 0 10px 6px; border: 1px solid var(--border); border-radius: var(--radius-l); background: var(--bg-soft); }
.plan-head { display: flex; align-items: center; gap: 6px; width: 100%; border: 0; background: none; padding: 7px 10px; font-size: 12.5px; color: var(--text-2); font-weight: 500; }
.plan-body { padding: 0 10px 8px; display: flex; flex-direction: column; gap: 4px; max-height: 180px; overflow: auto; }
.todo { display: flex; gap: 8px; font-size: 12.5px; color: var(--text-2); align-items: flex-start; line-height: 1.45; }
.todo.completed { color: var(--text-3); text-decoration: line-through; }
.todo.in_progress { color: var(--text); font-weight: 500; }
.box { width: 14px; height: 14px; border-radius: 4px; border: 1.5px solid var(--border-strong); display: flex; align-items: center; justify-content: center; flex: none; margin-top: 2px; }
.completed .box { background: var(--accent); border-color: var(--accent); color: #fff; }
.in_progress .box { border-color: var(--accent); }
.dot { width: 6px; height: 6px; border-radius: 50%; background: var(--accent); }
</style>
