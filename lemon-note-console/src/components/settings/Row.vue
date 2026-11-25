<script setup lang="ts">
import InfoTip from '@/components/ui/InfoTip.vue'
defineProps<{ label: string; tip?: string }>()
</script>

<template>
  <div class="srow">
    <span class="lbl">{{ label }}<InfoTip v-if="tip" :text="tip" /></span>
    <div class="ctl"><slot /></div>
  </div>
</template>

<style scoped>
.srow { display: flex; align-items: center; gap: 16px; min-height: 34px; }
.lbl { flex: 1; font-size: 13.5px; display: flex; align-items: center; gap: 4px; min-width: 0; }
.ctl { display: flex; align-items: center; gap: 8px; flex: none; }
@media (max-width: 760px) { .srow { flex-direction: column; align-items: stretch; gap: 6px; } }
</style>
