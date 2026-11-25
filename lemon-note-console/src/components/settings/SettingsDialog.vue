<script setup lang="ts">
import Modal from '@/components/ui/Modal.vue'
import { useUI } from '@/stores/ui'
const ui = useUI()
</script>

<template>
  <Modal v-model:open="ui.settingsOpen" :title="$t('settings.title')" :width="860" />
</template>
