<script setup lang="ts">
import { useToast } from '@/stores/toast'
import Icon from '@/components/icons/Icon.vue'
const toast = useToast()
const icons: Record<string, string> = { info: 'info', success: 'success', error: 'error' }
</script>

<template>
  <div class="toasts" aria-live="polite">
    <TransitionGroup name="toast">
      <div v-for="t in toast.items" :key="t.id" class="toast" :class="t.kind">
        <Icon :name="icons[t.kind]" :size="16" />
        <span class="text">{{ t.text }}</span>
        <button v-if="t.action" class="act" @click="t.action.run(); toast.dismiss(t.id)">{{ t.action.label }}</button>
      </div>
    </TransitionGroup>
  </div>
</template>

<style scoped>
.toasts { position: fixed; bottom: 20px; left: 50%; transform: translateX(-50%); z-index: 2000; display: flex; flex-direction: column; gap: 8px; align-items: center; pointer-events: none; }
.toast { pointer-events: auto; display: flex; align-items: center; gap: 10px; background: #2b2b2a; color: #f3f3f1; padding: 9px 14px; border-radius: var(--radius-m); box-shadow: var(--shadow-pop); max-width: min(560px, 92vw); font-size: 13.5px; }
html[data-theme="dark"] .toast { background: #3a3a39; }
.toast.error .ln-icon { color: #ff8a84; }
.toast.success .ln-icon { color: #6fd49c; }
.text { line-height: 1.45; }
.act { border: 0; background: rgba(255,255,255,.14); color: inherit; border-radius: 4px; padding: 3px 8px; font-size: 12.5px; }
.toast-enter-from, .toast-leave-to { opacity: 0; transform: translateY(6px); }
.toast-enter-active, .toast-leave-active { transition: all .16s var(--ease); }
</style>
