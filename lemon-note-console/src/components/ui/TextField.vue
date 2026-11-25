<script setup lang="ts">
import { ref } from 'vue'
const model = defineModel<string>({ default: '' })
withDefaults(defineProps<{ label?: string; type?: string; placeholder?: string; autocomplete?: string; autofocus?: boolean; error?: string; mono?: boolean; disabled?: boolean; size?: 'sm' | 'md' }>(), { type: 'text', size: 'md' })
const el = ref<HTMLInputElement>()
defineExpose({ focus: () => el.value?.focus(), select: () => el.value?.select() })
</script>

<template>
  <label class="field">
    <span v-if="label || $slots.label" class="label">{{ label }}<slot name="label" /></span>
    <input ref="el" v-model="model" class="input" :class="[size, { mono, invalid: !!error }]" :type="type" :placeholder="placeholder"
      :autocomplete="autocomplete" :autofocus="autofocus" :disabled="disabled" spellcheck="false" />
    <span v-if="error" class="error">{{ error }}</span>
  </label>
</template>

<style scoped>
.field { display: flex; flex-direction: column; gap: 6px; min-width: 0; }
.label { font-size: 12.5px; font-weight: 500; color: var(--text-2); display: inline-flex; align-items: center; gap: 4px; }
.input { height: 34px; padding: 0 10px; border-radius: var(--radius-m); border: 1px solid var(--border-strong); background: var(--bg); outline: none;
  transition: border-color .12s, box-shadow .12s; width: 100%; }
.input.sm { height: 30px; font-size: 13px; }
.input:focus { border-color: var(--accent); box-shadow: 0 0 0 3px var(--accent-soft); }
.input.invalid { border-color: var(--danger); }
.input.mono { font-family: var(--font-mono); font-size: 13px; }
.input:disabled { background: var(--bg-soft); color: var(--text-2); }
.error { font-size: 12px; color: var(--danger); }
</style>
