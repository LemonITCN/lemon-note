<script setup lang="ts">
import { SwitchRoot, SwitchThumb } from 'reka-ui'
const model = defineModel<boolean>({ default: false })
defineProps<{ disabled?: boolean }>()
</script>

<template>
  <SwitchRoot v-model="model" class="sw" :disabled="disabled"><SwitchThumb class="thumb" /></SwitchRoot>
</template>

<style scoped>
.sw { width: 32px; height: 18px; border-radius: 10px; border: 0; background: var(--border-strong); position: relative; flex: none; padding: 0; transition: background .15s; }
.sw[data-state="checked"] { background: var(--accent); }
.sw:disabled { opacity: .5; }
:deep(.thumb) { display: block; width: 14px; height: 14px; border-radius: 50%; background: #fff; box-shadow: 0 1px 2px rgba(0,0,0,.25); transform: translateX(2px); transition: transform .15s var(--ease); }
:deep(.thumb[data-state="checked"]) { transform: translateX(16px); }
</style>
