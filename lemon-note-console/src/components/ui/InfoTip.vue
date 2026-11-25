<script setup lang="ts">
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
defineProps<{ text?: string; width?: number }>()
</script>

<template>
  <PopoverRoot>
    <PopoverTrigger class="info-trigger" :aria-label="'info'"><Icon name="info" :size="14" /></PopoverTrigger>
    <PopoverPortal>
      <PopoverContent class="info-pop" side="top" :side-offset="6" :collision-padding="12" :style="{ maxWidth: (width || 280) + 'px' }">
        <slot>{{ text }}</slot>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<style>
.info-trigger { display: inline-flex; border: 0; background: none; padding: 1px; color: var(--text-3); border-radius: 50%; vertical-align: middle; }
.info-trigger:hover { color: var(--text-2); }
.info-pop { background: var(--bg-elev); color: var(--text); box-shadow: var(--shadow-pop); border-radius: var(--radius-m); padding: 10px 12px;
  font-size: 12.5px; line-height: 1.55; z-index: 1100; font-weight: 400; }
</style>
