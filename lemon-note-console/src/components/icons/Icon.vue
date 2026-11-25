<script setup lang="ts">
import { computed } from 'vue'
import { icons } from './icons'

const props = withDefaults(defineProps<{ name: string; size?: number | string; stroke?: number }>(), { size: 18, stroke: 1.6 })
const markup = computed(() => icons[props.name] ?? icons['page-empty'])
</script>

<template>
  <svg
    class="ln-icon"
    :width="size"
    :height="size"
    viewBox="0 0 24 24"
    fill="none"
    stroke="currentColor"
    :stroke-width="stroke"
    stroke-linecap="round"
    stroke-linejoin="round"
    aria-hidden="true"
    v-html="markup"
  />
</template>

<style scoped>
.ln-icon { flex: none; display: block; }
</style>
