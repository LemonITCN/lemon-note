<script setup lang="ts">
import { onMounted, onBeforeUnmount } from 'vue'
import Btn from './Btn.vue'
const emit = defineEmits<{ close: [] }>()
defineProps<{ title?: string }>()
function onKey(e: KeyboardEvent) { if (e.key === 'Escape') { e.stopPropagation(); emit('close') } }
onMounted(() => window.addEventListener('keydown', onKey, true))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey, true))
</script>

<template>
  <Teleport to="body">
    <div class="fs">
      <div class="fs-bar">
        <span class="fs-title">{{ title }}</span>
        <span class="ln-spacer" />
        <slot name="actions" />
        <Btn variant="primary" size="sm" @click="emit('close')">{{ $t('common.done') }}</Btn>
      </div>
      <div class="fs-body"><slot /></div>
    </div>
  </Teleport>
</template>

<style scoped>
.fs { position: fixed; inset: 0; z-index: 900; background: var(--bg); display: flex; flex-direction: column; }
.fs-bar { display: flex; align-items: center; gap: 8px; height: 48px; padding: 0 12px 0 18px; border-bottom: 1px solid var(--border); flex: none; }
.fs-title { font-weight: 600; }
.fs-body { flex: 1; min-height: 0; position: relative; }
</style>
