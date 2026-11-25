<script setup lang="ts">
import PageView from '@/components/page/PageView.vue'
defineProps<{ id: string }>()
</script>

<template>
  <PageView :id="id" />
</template>
