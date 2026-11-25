<script setup lang="ts">
// 斜杠菜单与 @ 提及共用的浮动列表。
import { computed, watch, ref, nextTick } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import PageIcon from '@/components/page/PageIcon.vue'

interface Item { label: string; icon?: string; group?: string; sub?: string; kind?: string }
const props = defineProps<{ open: boolean; items: Item[]; index: number; rect: DOMRect | null; empty?: string }>()
const emit = defineEmits<{ pick: [i: number]; hover: [i: number] }>()
const list = ref<HTMLDivElement>()

const style = computed(() => {
  if (!props.rect) return { display: 'none' }
  const h = Math.min(360, props.items.length * 36 + 40)
  const below = props.rect.bottom + h + 12 < window.innerHeight
  const left = Math.min(props.rect.left, window.innerWidth - 300)
  return below ? { left: left + 'px', top: props.rect.bottom + 6 + 'px' } : { left: left + 'px', bottom: window.innerHeight - props.rect.top + 6 + 'px' }
})

watch(() => props.index, async () => {
  await nextTick()
  list.value?.querySelector('.sel')?.scrollIntoView({ block: 'nearest' })
})

function header(i: number) { return props.items[i].group && (i === 0 || props.items[i - 1].group !== props.items[i].group) ? props.items[i].group : '' }
</script>

<template>
  <Teleport to="body">
    <div v-if="open" ref="list" class="fl" :style="style" @mousedown.prevent>
      <template v-for="(it, i) in items" :key="i">
        <div v-if="header(i)" class="fl-group">{{ header(i) }}</div>
        <div class="fl-item" :class="{ sel: i === index }" @mouseenter="emit('hover', i)" @click="emit('pick', i)">
          <span class="fl-icon">
            <PageIcon v-if="it.kind === 'page'" :icon="it.icon" :size="18" />
            <Icon v-else :name="it.icon || (it.kind === 'user' ? 'user' : it.kind === 'date' ? 'calendar' : 'text')" :size="18" />
          </span>
          <span class="fl-label ln-ellipsis">{{ it.label }}</span>
          <span v-if="it.sub" class="fl-sub">{{ it.sub }}</span>
        </div>
      </template>
      <div v-if="!items.length" class="fl-empty">{{ empty }}</div>
    </div>
  </Teleport>
</template>

<style>
.fl { position: fixed; z-index: 1300; width: 280px; max-height: 360px; overflow-y: auto; background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); padding: 4px; }
.fl-group { font-size: 11.5px; color: var(--text-3); padding: 8px 8px 4px; font-weight: 500; }
.fl-item { display: flex; align-items: center; gap: 10px; height: 34px; padding: 0 8px; border-radius: 6px; cursor: pointer; }
.fl-item.sel { background: var(--bg-hover); }
.fl-icon { width: 26px; height: 26px; border: 1px solid var(--border); border-radius: 6px; display: flex; align-items: center; justify-content: center; color: var(--text-2); background: var(--bg); flex: none; }
.fl-label { flex: 1; }
.fl-sub { font-size: 12px; color: var(--text-3); }
.fl-empty { color: var(--text-3); padding: 10px; font-size: 13px; }
</style>
