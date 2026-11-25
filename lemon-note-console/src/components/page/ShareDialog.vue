<script setup lang="ts">
// 分享：给本系统用户分享、生成公开链接（完整实现见分享模块）。
const open = defineModel<boolean>('open', { default: false })
defineProps<{ pageId: string; pageTitle: string; role: string }>()
import Modal from '@/components/ui/Modal.vue'
</script>

<template>
  <Modal v-model:open="open" :title="$t('page.share')" :width="520" />
</template>
