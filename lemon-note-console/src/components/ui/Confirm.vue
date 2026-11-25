<script setup lang="ts">
import Modal from './Modal.vue'
import Btn from './Btn.vue'
import { useConfirm } from '@/lib/confirm'
const c = useConfirm()
</script>

<template>
  <Modal v-model:open="c.state.open" :title="c.state.title" :width="400" @update:open="(v: boolean) => !v && c.resolve(false)">
    <p class="msg">{{ c.state.message }}</p>
    <template #footer>
      <Btn @click="c.resolve(false)">{{ $t('common.cancel') }}</Btn>
      <Btn :variant="c.state.danger ? 'danger' : 'primary'" @click="c.resolve(true)">{{ c.state.okText || $t('common.confirm') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.msg { margin: 0; color: var(--text-2); line-height: 1.6; white-space: pre-wrap; }
</style>
