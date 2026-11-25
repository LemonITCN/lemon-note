<script setup lang="ts">
// 贴着某个元素弹出的浮层（单元格编辑器等），点外面或按 Esc 关闭。
import { ref, computed, onMounted, onBeforeUnmount } from 'vue'
const props = defineProps<{ anchor: DOMRect | null }>()
const emit = defineEmits<{ close: [] }>()
const el = ref<HTMLDivElement>()
const style = computed(() => {
  const a = props.anchor
  if (!a) return {}
  const top = a.bottom + 4 + 320 > window.innerHeight ? Math.max(8, a.top - 4 - 320) : a.bottom + 4
  return { left: Math.min(a.left, window.innerWidth - 300) + 'px', top: top + 'px' }
})
function down(e: MouseEvent) { if (el.value && !el.value.contains(e.target as Node)) emit('close') }
function key(e: KeyboardEvent) { if (e.key === 'Escape') emit('close') }
onMounted(() => { setTimeout(() => window.addEventListener('mousedown', down), 0); window.addEventListener('keydown', key) })
onBeforeUnmount(() => { window.removeEventListener('mousedown', down); window.removeEventListener('keydown', key) })
</script>

<template>
  <Teleport to="body">
    <div ref="el" class="fp" :style="style"><slot /></div>
  </Teleport>
</template>

<style scoped>
.fp { position: fixed; z-index: 1250; background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); padding: 8px; }
</style>
