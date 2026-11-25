<script setup lang="ts">
import { computed } from 'vue'
const props = defineProps<{ pct: number; title?: string }>()
const r = 7
const c = 2 * Math.PI * r
const dash = computed(() => `${(Math.min(100, props.pct) / 100) * c} ${c}`)
</script>

<template>
  <span class="ring" :title="title">
    <svg width="18" height="18" viewBox="0 0 18 18"><circle cx="9" cy="9" :r="r" class="bg" /><circle cx="9" cy="9" :r="r" class="fg" :class="{ warn: pct > 80 }" :stroke-dasharray="dash" transform="rotate(-90 9 9)" /></svg>
    <span class="num">{{ pct }}%</span>
  </span>
</template>

<style scoped>
.ring { display: inline-flex; align-items: center; gap: 4px; font-size: 11.5px; color: var(--text-3); }
circle { fill: none; stroke-width: 2.2; }
.bg { stroke: var(--border-strong); }
.fg { stroke: var(--accent); stroke-linecap: round; transition: stroke-dasharray .3s; }
.fg.warn { stroke: var(--warning); }
</style>
