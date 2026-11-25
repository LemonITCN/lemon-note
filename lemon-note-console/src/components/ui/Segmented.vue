<script setup lang="ts">
const model = defineModel<string>({ default: '' })
defineProps<{ options: { value: string; label: string; icon?: string }[] }>()
import Icon from '@/components/icons/Icon.vue'
</script>

<template>
  <div class="seg" role="radiogroup">
    <button v-for="o in options" :key="o.value" type="button" role="radio" :aria-checked="model === o.value" :class="{ on: model === o.value }" @click="model = o.value">
      <Icon v-if="o.icon" :name="o.icon" :size="15" /><span>{{ o.label }}</span>
    </button>
  </div>
</template>

<style scoped>
.seg { display: inline-flex; background: var(--bg-sunken); border-radius: var(--radius-m); padding: 2px; gap: 2px; }
.seg button { display: inline-flex; align-items: center; gap: 6px; border: 0; background: transparent; color: var(--text-2); height: 28px; padding: 0 12px; border-radius: 5px; font-size: 13px; }
.seg button.on { background: var(--bg-elev); color: var(--text); box-shadow: var(--shadow-soft); }
</style>
