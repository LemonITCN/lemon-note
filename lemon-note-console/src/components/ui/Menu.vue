<script setup lang="ts">
// 下拉菜单：items 描述菜单项，触发器放在默认插槽里。
import { DropdownMenuRoot, DropdownMenuTrigger, DropdownMenuPortal, DropdownMenuContent, DropdownMenuItem, DropdownMenuSeparator, DropdownMenuLabel } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'

export interface MenuItem { label?: string; icon?: string; danger?: boolean; disabled?: boolean; hint?: string; separator?: boolean; heading?: string; checked?: boolean; run?: () => void }
defineProps<{ items: MenuItem[]; align?: 'start' | 'center' | 'end'; side?: 'top' | 'bottom' | 'left' | 'right'; width?: number }>()
const open = defineModel<boolean>('open', { default: false })
</script>

<template>
  <DropdownMenuRoot v-model:open="open" :modal="false">
    <DropdownMenuTrigger as-child><slot /></DropdownMenuTrigger>
    <DropdownMenuPortal>
      <DropdownMenuContent class="ln-menu" :align="align || 'start'" :side="side || 'bottom'" :side-offset="4" :collision-padding="8" :style="{ minWidth: (width || 200) + 'px' }">
        <template v-for="(it, i) in items" :key="i">
          <DropdownMenuSeparator v-if="it.separator" class="ln-menu-sep" />
          <DropdownMenuLabel v-else-if="it.heading" class="ln-menu-heading">{{ it.heading }}</DropdownMenuLabel>
          <DropdownMenuItem v-else class="ln-menu-item" :class="{ danger: it.danger }" :disabled="it.disabled" @select="it.run?.()">
            <Icon v-if="it.icon" :name="it.icon" :size="16" />
            <span class="ln-menu-label">{{ it.label }}</span>
            <Icon v-if="it.checked" name="check" :size="15" class="ln-menu-check" />
            <span v-else-if="it.hint" class="ln-menu-hint">{{ it.hint }}</span>
          </DropdownMenuItem>
        </template>
      </DropdownMenuContent>
    </DropdownMenuPortal>
  </DropdownMenuRoot>
</template>

<style>
.ln-menu { background: var(--bg-elev); border-radius: var(--radius-m); box-shadow: var(--shadow-pop); padding: 4px; z-index: 1200; max-height: 70vh; overflow: auto; animation: ln-menu-in .1s; }
.ln-menu-item { display: flex; align-items: center; gap: 8px; height: 30px; padding: 0 8px; border-radius: 4px; color: var(--text); cursor: pointer; outline: none; font-size: 13.5px; user-select: none; }
.ln-menu-item[data-highlighted] { background: var(--bg-hover); }
.ln-menu-item[data-disabled] { opacity: .45; cursor: default; }
.ln-menu-item .ln-icon { color: var(--text-2); }
.ln-menu-item.danger, .ln-menu-item.danger .ln-icon { color: var(--danger); }
.ln-menu-label { flex: 1; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.ln-menu-hint { color: var(--text-3); font-size: 12px; }
.ln-menu-check { color: var(--accent) !important; }
.ln-menu-sep { height: 1px; background: var(--border); margin: 4px 0; }
.ln-menu-heading { font-size: 11.5px; color: var(--text-3); padding: 6px 8px 2px; font-weight: 500; }
@keyframes ln-menu-in { from { opacity: 0; transform: translateY(-2px); } }
</style>
