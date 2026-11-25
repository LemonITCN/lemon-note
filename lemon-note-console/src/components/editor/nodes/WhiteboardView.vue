<script setup lang="ts">
import { ref, onMounted, watch, nextTick } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import Fullscreen from '@/components/ui/Fullscreen.vue'
import { emptyBoard, mountBoard, boardSVG, type BoardData } from '@/lib/excalidraw'
import { sanitizeSVG } from '@/lib/sanitize'
import { useUI } from '@/stores/ui'
import { useI18n } from 'vue-i18n'

const props = defineProps(nodeViewProps)
const ui = useUI()
const { locale } = useI18n()
const svg = ref('')
const editing = ref(false)
const host = ref<HTMLDivElement>()
const title = ref(props.node.attrs.title || '')
let board: Awaited<ReturnType<typeof mountBoard>> | null = null
let timer: number | undefined

function data(): BoardData { return (props.node.attrs.data as BoardData) || emptyBoard() }

async function refresh() {
  try { svg.value = sanitizeSVG(await boardSVG(data(), ui.effectiveTheme)) } catch { svg.value = '' }
}

async function open() {
  if (!props.editor.isEditable) return
  editing.value = true
  await nextTick()
  board = await mountBoard(host.value!, data(), { theme: ui.effectiveTheme, lang: locale.value, onChange: (d) => {
    clearTimeout(timer)
    timer = window.setTimeout(() => props.updateAttributes({ data: d }), 800)
  } })
}

function close() {
  clearTimeout(timer)
  const snap = board?.snapshot()
  if (snap) props.updateAttributes({ data: snap, title: title.value })
  board?.unmount()
  board = null
  editing.value = false
  refresh()
}

watch(() => props.node.attrs.data, () => { if (!editing.value) refresh() })
watch(() => ui.effectiveTheme, refresh)
onMounted(refresh)
</script>

<template>
  <NodeViewWrapper class="ln-board" :class="{ selected: props.selected }">
    <div class="bar" contenteditable="false">
      <Icon name="whiteboard" :size="14" /><span class="ln-ellipsis">{{ props.node.attrs.title || $t('editor.whiteboard') }}</span><span class="ln-spacer" />
      <button v-if="props.editor.isEditable" class="tb" @click="open"><Icon name="expand" :size="14" />{{ $t('common.edit') }}</button>
    </div>
    <div class="canvas" contenteditable="false" @dblclick="open">
      <div v-if="svg" class="svg" v-html="svg" />
      <div v-else class="empty" @click="open">{{ $t('editor.whiteboard') }}</div>
    </div>
    <Fullscreen v-if="editing" :title="title || $t('editor.whiteboard')" @close="close">
      <template #actions><input v-model="title" class="title-input" :placeholder="$t('editor.whiteboard')" /></template>
      <div ref="host" class="host" />
    </Fullscreen>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-board { border: 1px solid var(--border); border-radius: var(--radius-l); margin: 6px 0; overflow: hidden; background: var(--bg); }
.ln-board.selected { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.bar { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 6px 0 12px; font-size: 12px; color: var(--text-3); border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.tb { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12px; padding: 3px 6px; border-radius: 4px; }
.tb:hover { background: var(--bg-hover); color: var(--text); }
.canvas { min-height: 160px; max-height: 480px; overflow: hidden; display: flex; align-items: center; justify-content: center; padding: 12px; }
.svg :deep(svg) { max-width: 100%; max-height: 456px; height: auto; }
.empty { color: var(--text-3); cursor: pointer; }
.host { position: absolute; inset: 0; }
.title-input { border: 1px solid var(--border-strong); border-radius: var(--radius-m); height: 28px; padding: 0 8px; background: var(--bg); outline: none; width: 220px; }
</style>
