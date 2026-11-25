<script setup lang="ts">
import { ref, computed } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'

const props = defineProps(nodeViewProps)
const url = ref('')
const safe = computed(() => /^https:\/\//i.test(props.node.attrs.url || '') ? props.node.attrs.url : '')

function startResize(e: PointerEvent) {
  e.preventDefault()
  const y0 = e.clientY
  const h0 = props.node.attrs.height || 400
  const move = (ev: PointerEvent) => props.updateAttributes({ height: Math.max(120, Math.min(1600, h0 + ev.clientY - y0)) })
  const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up) }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
</script>

<template>
  <NodeViewWrapper class="ln-embed" :class="{ selected: props.selected }">
    <div v-if="safe" class="frame" contenteditable="false" :style="{ height: (props.node.attrs.height || 400) + 'px' }">
      <iframe :src="safe" sandbox="allow-scripts allow-same-origin allow-popups allow-forms allow-presentation" referrerpolicy="no-referrer" loading="lazy" allowfullscreen />
      <span v-if="props.editor.isEditable" class="grip" @pointerdown="startResize" />
    </div>
    <div v-else class="empty" contenteditable="false">
      <Icon name="globe" :size="17" />
      <input v-model="url" placeholder="https://" :disabled="!props.editor.isEditable" @keydown.enter.prevent="/^https:\/\//i.test(url) && props.updateAttributes({ url: url.trim() })" />
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-embed { margin: 6px 0; }
.frame { position: relative; border: 1px solid var(--border); border-radius: var(--radius-m); overflow: hidden; }
.ln-embed.selected .frame { border-color: var(--accent); }
iframe { width: 100%; height: 100%; border: 0; display: block; background: #fff; }
.grip { position: absolute; left: 50%; bottom: 2px; width: 48px; height: 6px; margin-left: -24px; border-radius: 4px; background: var(--border-strong); cursor: row-resize; }
.empty { display: flex; align-items: center; gap: 8px; background: var(--bg-soft); border-radius: var(--radius-m); padding: 8px 12px; color: var(--text-3); }
.empty input { flex: 1; border: 0; background: none; outline: none; }
</style>
