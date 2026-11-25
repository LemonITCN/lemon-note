<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
withDefaults(defineProps<{ variant?: 'primary' | 'secondary' | 'ghost' | 'danger'; size?: 'sm' | 'md'; icon?: string; loading?: boolean; disabled?: boolean; block?: boolean; type?: 'button' | 'submit' }>(), { variant: 'secondary', size: 'md', type: 'button' })
</script>

<template>
  <button :type="type" class="btn" :class="[variant, size, { block, loading }]" :disabled="disabled || loading">
    <span v-if="loading" class="spin" />
    <Icon v-else-if="icon" :name="icon" :size="size === 'sm' ? 14 : 16" />
    <span v-if="$slots.default" class="label"><slot /></span>
  </button>
</template>

<style scoped>
.btn { display: inline-flex; align-items: center; justify-content: center; gap: 6px; border-radius: var(--radius-m); border: 1px solid transparent;
  height: 32px; padding: 0 12px; font-weight: 500; white-space: nowrap; transition: background .12s, border-color .12s, color .12s; user-select: none; }
.btn.sm { height: 28px; padding: 0 10px; font-size: 13px; }
.btn.block { display: flex; width: 100%; }
.btn:disabled { opacity: .5; cursor: default; }
.primary { background: var(--accent); color: #fff; }
.primary:not(:disabled):hover { background: var(--accent-hover); }
html[data-theme="dark"][data-accent="graphite"] .primary { color: #191919; }
.secondary { background: var(--bg); border-color: var(--border-strong); color: var(--text); }
.secondary:not(:disabled):hover { background: var(--bg-hover); }
.ghost { background: transparent; color: var(--text-2); }
.ghost:not(:disabled):hover { background: var(--bg-hover); color: var(--text); }
.danger { background: var(--danger); color: #fff; }
.danger:not(:disabled):hover { filter: brightness(.95); }
.spin { width: 14px; height: 14px; border-radius: 50%; border: 2px solid currentColor; border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
.label { display: inline-flex; align-items: center; }
</style>
