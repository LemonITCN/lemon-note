<script setup lang="ts">
import { ref, onMounted, onBeforeUnmount, watch, nextTick, shallowRef } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import Fullscreen from '@/components/ui/Fullscreen.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { defaultMind, toSMM, fromSMM, type MindData } from '@/lib/mindmap'
import { useUI } from '@/stores/ui'
import { useI18n } from 'vue-i18n'

const props = defineProps(nodeViewProps)
const ui = useUI()
const { t } = useI18n()
const preview = ref<HTMLDivElement>()
const full = ref<HTMLDivElement>()
const editing = ref(false)
const layout = ref<string>((props.node.attrs.data as MindData)?.layout || 'logicalStructure')
const inst = shallowRef<any>(null)
const fullInst = shallowRef<any>(null)
let MindMap: any = null

const layouts = [
  { value: 'logicalStructure', label: t('editor.mindmap') + ' →' },
  { value: 'mindMap', label: '⇆' },
  { value: 'organizationStructure', label: '↓' },
  { value: 'catalogOrganization', label: '☰' },
]

function data(): MindData { return (props.node.attrs.data as MindData) || defaultMind(t('editor.mindmap')) }

async function load() {
  if (!MindMap) MindMap = (await import('simple-mind-map')).default
  return MindMap
}

function themeName() { return ui.effectiveTheme === 'dark' ? 'dark' : 'default' }

async function mountPreview() {
  const MM = await load()
  if (!preview.value) return
  inst.value?.destroy()
  const d = data()
  inst.value = new MM({ el: preview.value, data: toSMM(d.root), layout: d.layout || 'logicalStructure', theme: themeName(), readonly: true,
    enableFreeDrag: false, mousewheelAction: 'move', fit: true })
  setTimeout(() => inst.value?.view?.fit?.(), 50)
}

async function openEditor() {
  if (!props.editor.isEditable) return
  editing.value = true
  await nextTick()
  const MM = await load()
  const d = data()
  fullInst.value = new MM({ el: full.value, data: toSMM(d.root), layout: d.layout || 'logicalStructure', theme: themeName(), readonly: false })
  fullInst.value.on('data_change', (tree: any) => {
    props.updateAttributes({ data: { ...data(), root: fromSMM(tree), layout: layout.value } })
  })
}

function closeEditor() {
  if (fullInst.value) {
    const tree = fullInst.value.getData(false)
    props.updateAttributes({ data: { ...data(), root: fromSMM(tree), layout: layout.value } })
    fullInst.value.destroy()
    fullInst.value = null
  }
  editing.value = false
  nextTick(mountPreview)
}

watch(layout, (v) => fullInst.value?.setLayout(v))
watch(() => props.node.attrs.data, () => { if (!editing.value) mountPreview() }, { deep: true })
watch(() => ui.effectiveTheme, () => { mountPreview(); fullInst.value?.setTheme(themeName()) })
onMounted(() => {
  if (!props.node.attrs.data && props.editor.isEditable) props.updateAttributes({ data: defaultMind(t('editor.mindmap')) })
  mountPreview()
})
onBeforeUnmount(() => { inst.value?.destroy(); fullInst.value?.destroy() })
</script>

<template>
  <NodeViewWrapper class="ln-board" :class="{ selected: props.selected }">
    <div class="bar" contenteditable="false">
      <Icon name="mindmap" :size="14" /><span>{{ $t('editor.mindmap') }}</span><span class="ln-spacer" />
      <button v-if="props.editor.isEditable" class="tb" @click="openEditor"><Icon name="expand" :size="14" />{{ $t('common.edit') }}</button>
    </div>
    <div ref="preview" class="canvas" contenteditable="false" @dblclick="openEditor" />
    <Fullscreen v-if="editing" :title="$t('editor.mindmap')" @close="closeEditor">
      <template #actions><Segmented v-model="layout" :options="layouts" /></template>
      <div ref="full" class="full-canvas" />
    </Fullscreen>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-board { border: 1px solid var(--border); border-radius: var(--radius-l); margin: 6px 0; overflow: hidden; background: var(--bg); }
.ln-board.selected { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.bar { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 6px 0 12px; font-size: 12px; color: var(--text-3); border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.tb { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12px; padding: 3px 6px; border-radius: 4px; }
.tb:hover { background: var(--bg-hover); color: var(--text); }
.canvas { height: 360px; position: relative; }
.full-canvas { position: absolute; inset: 0; }
</style>
