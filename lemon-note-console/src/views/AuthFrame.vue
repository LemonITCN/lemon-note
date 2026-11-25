<script setup lang="ts">
import Icon from '@/components/icons/Icon.vue'
defineProps<{ title: string; subtitle?: string; wide?: boolean }>()
</script>

<template>
  <div class="auth">
    <div class="card" :class="{ wide }">
      <div class="brand"><Icon name="logo" :size="26" :stroke="1.5" /><span>Lemon Note</span></div>
      <h1>{{ title }}</h1>
      <p v-if="subtitle" class="sub">{{ subtitle }}</p>
      <slot />
    </div>
  </div>
</template>

<style scoped>
.auth { min-height: 100%; display: flex; align-items: flex-start; justify-content: center; padding: 10vh 16px 40px; background: var(--bg-soft); }
.card { width: 380px; max-width: 100%; background: var(--bg-elev); border: 1px solid var(--border); border-radius: 14px; padding: 28px 28px 24px; box-shadow: var(--shadow-soft); }
.card.wide { width: 560px; }
.brand { display: flex; align-items: center; gap: 8px; font-weight: 600; color: var(--text); margin-bottom: 22px; letter-spacing: .2px; }
h1 { font-size: 20px; font-weight: 600; margin: 0 0 4px; }
.sub { color: var(--text-2); margin: 0 0 18px; }
@media (max-width: 760px) { .auth { padding-top: 6vh; } .card { padding: 22px 18px; } }
</style>
