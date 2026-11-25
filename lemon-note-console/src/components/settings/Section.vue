<script setup lang="ts">
import InfoTip from '@/components/ui/InfoTip.vue'
defineProps<{ title: string; tip?: string }>()
</script>

<template>
  <section class="sec">
    <h3>{{ title }}<InfoTip v-if="tip" :text="tip" /></h3>
    <div class="body"><slot /></div>
  </section>
</template>

<style scoped>
.sec { padding: 0 0 22px; margin-bottom: 18px; border-bottom: 1px solid var(--border); }
.sec:last-child { border-bottom: 0; }
h3 { font-size: 14px; font-weight: 600; margin: 0 0 12px; display: flex; align-items: center; gap: 6px; }
.body { display: flex; flex-direction: column; gap: 12px; }
</style>
