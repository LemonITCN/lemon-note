<script setup lang="ts">
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import { api } from '@/api/client'
import { useWorkspace } from '@/stores/workspace'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const open = defineModel<boolean>('open', { default: false })
const { t } = useI18n()
const ws = useWorkspace()
const toast = useToast()
const name = ref('')
const busy = ref(false)

async function create() {
  busy.value = true
  try {
    const r = await api.post<any>('/api/spaces', { name: name.value })
    await ws.loadSpaces()
    ws.setSpace(r.space.id)
    open.value = false
    name.value = ''
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <Modal v-model:open="open" :title="t('nav.newSpace')" :width="400">
    <form @submit.prevent="create"><TextField v-model="name" :label="t('common.name')" autofocus /></form>
    <template #footer>
      <Btn @click="open = false">{{ t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="busy" :disabled="!name.trim()" @click="create">{{ t('common.create') }}</Btn>
    </template>
  </Modal>
</template>
