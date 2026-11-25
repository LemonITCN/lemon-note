<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
withDefaults(defineProps<{ icon: string; label?: string; size?: number; active?: boolean; disabled?: boolean }>(), { size: 18 })
</script>

<template>
  <button type="button" class="icon-btn" :class="{ active }" :title="label" :aria-label="label" :disabled="disabled">
    <Icon :name="icon" :size="size" />
  </button>
</template>

<style scoped>
.icon-btn { display: inline-flex; align-items: center; justify-content: center; width: 28px; height: 28px; border: 0; border-radius: var(--radius-m);
  background: transparent; color: var(--text-2); flex: none; transition: background .12s, color .12s; }
.icon-btn:hover:not(:disabled) { background: var(--bg-hover); color: var(--text); }
.icon-btn.active { background: var(--bg-active); color: var(--text); }
.icon-btn:disabled { opacity: .4; cursor: default; }
</style>
