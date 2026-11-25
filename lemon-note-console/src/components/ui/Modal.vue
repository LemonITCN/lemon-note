<script setup lang="ts">
import { DialogRoot, DialogPortal, DialogOverlay, DialogContent, DialogTitle, DialogDescription, DialogClose } from 'reka-ui'
import IconBtn from './IconBtn.vue'
const open = defineModel<boolean>('open', { default: false })
withDefaults(defineProps<{ title?: string; width?: number | string; padded?: boolean; hideClose?: boolean }>(), { width: 440, padded: true })
</script>

<template>
  <DialogRoot v-model:open="open">
    <DialogPortal>
      <DialogOverlay class="overlay" />
      <DialogContent class="modal" :style="{ width: typeof width === 'number' ? width + 'px' : width }" :aria-describedby="undefined">
        <div v-if="title || !hideClose" class="head">
          <DialogTitle class="title">{{ title }}</DialogTitle>
          <DialogClose v-if="!hideClose" as-child><IconBtn icon="close" :label="$t('common.close')" /></DialogClose>
        </div>
        <DialogDescription v-if="false" />
        <div :class="{ body: padded }"><slot /></div>
        <div v-if="$slots.footer" class="foot"><slot name="footer" /></div>
      </DialogContent>
    </DialogPortal>
  </DialogRoot>
</template>

<style scoped>
:deep(.overlay) { position: fixed; inset: 0; background: var(--overlay); z-index: 1000; animation: fade .12s; }
:deep(.modal) { position: fixed; left: 50%; top: 12vh; transform: translateX(-50%); max-width: calc(100vw - 24px); max-height: 76vh; display: flex; flex-direction: column;
  background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); z-index: 1001; outline: none; animation: pop .14s var(--ease); }
.head { display: flex; align-items: center; justify-content: space-between; padding: 14px 14px 6px 20px; gap: 8px; }
.title { font-size: 15px; font-weight: 600; margin: 0; }
.body { padding: 8px 20px 20px; overflow: auto; }
.foot { display: flex; justify-content: flex-end; gap: 8px; padding: 12px 20px 16px; border-top: 1px solid var(--border); }
@keyframes fade { from { opacity: 0; } }
@keyframes pop { from { opacity: 0; transform: translate(-50%, 6px) scale(.985); } }
@media (max-width: 760px) { :deep(.modal) { top: auto; bottom: 0; left: 0; right: 0; transform: none; width: 100% !important; max-width: 100%; max-height: 88vh; border-radius: 14px 14px 0 0; animation: none; } }
</style>
