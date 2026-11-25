<script setup lang="ts">
// 图表：静态 ECharts 配置，或绑定数据库视图（source.kind = database，由服务端算好数据）。
import { ref, onMounted, onBeforeUnmount, watch, nextTick, shallowRef } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import { useUI } from '@/stores/ui'
import { api } from '@/api/client'

const props = defineProps(nodeViewProps)
const ui = useUI()
const el = ref<HTMLDivElement>()
const chart = shallowRef<any>(null)
const error = ref('')
const editing = ref(false)
const draft = ref('')
let ro: ResizeObserver | null = null

const sample = { type: 'bar', title: '', source: { kind: 'static', option: { xAxis: { type: 'category', data: ['A', 'B', 'C'] }, yAxis: { type: 'value' }, series: [{ type: 'bar', data: [3, 7, 5] }] } } }

async function option(): Promise<any> {
  const d = props.node.attrs.data || sample
  if (d.source?.kind === 'database') {
    const r = await api.post('/api/charts/query', d)
    return r.option
  }
  return { ...(d.source?.option || {}), ...(d.title ? { title: { text: d.title, left: 'center', textStyle: { fontSize: 14 } } } : {}) }
}

async function render() {
  if (!el.value) return
  try {
    const echarts = await import('echarts')
    chart.value?.dispose()
    chart.value = echarts.init(el.value, ui.effectiveTheme === 'dark' ? 'dark' : undefined, { renderer: 'svg' })
    const opt = await option()
    chart.value.setOption({ backgroundColor: 'transparent', ...opt })
    error.value = ''
  } catch (e: any) {
    error.value = String(e?.message || e)
  }
}

function edit() {
  if (!props.editor.isEditable) return
  draft.value = JSON.stringify(props.node.attrs.data || sample, null, 2)
  editing.value = true
}

function save() {
  try {
    props.updateAttributes({ data: JSON.parse(draft.value) })
    editing.value = false
    nextTick(render)
  } catch (e: any) { error.value = e.message }
}

watch(() => props.node.attrs.data, render, { deep: true })
watch(() => ui.effectiveTheme, render)
onMounted(() => {
  if (!props.node.attrs.data && props.editor.isEditable) props.updateAttributes({ data: sample })
  render()
  ro = new ResizeObserver(() => chart.value?.resize())
  if (el.value) ro.observe(el.value)
})
onBeforeUnmount(() => { ro?.disconnect(); chart.value?.dispose() })
</script>

<template>
  <NodeViewWrapper class="ln-board" :class="{ selected: props.selected }">
    <div class="bar" contenteditable="false">
      <Icon name="chart" :size="14" /><span>{{ $t('editor.chart') }}</span><span class="ln-spacer" />
      <button v-if="props.editor.isEditable" class="tb" @click="edit"><Icon name="code" :size="14" />{{ $t('common.edit') }}</button>
    </div>
    <div ref="el" class="chart" contenteditable="false" />
    <p v-if="error" class="err">{{ error }}</p>
    <Modal v-model:open="editing" :title="$t('editor.chart')" :width="640">
      <textarea v-model="draft" class="json" spellcheck="false" />
      <template #footer><Btn @click="editing = false">{{ $t('common.cancel') }}</Btn><Btn variant="primary" @click="save">{{ $t('common.save') }}</Btn></template>
    </Modal>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-board { border: 1px solid var(--border); border-radius: var(--radius-l); margin: 6px 0; overflow: hidden; background: var(--bg); }
.ln-board.selected { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.bar { display: flex; align-items: center; gap: 6px; height: 30px; padding: 0 6px 0 12px; font-size: 12px; color: var(--text-3); border-bottom: 1px solid var(--border); background: var(--bg-soft); }
.tb { display: inline-flex; align-items: center; gap: 4px; border: 0; background: none; color: var(--text-2); font-size: 12px; padding: 3px 6px; border-radius: 4px; }
.tb:hover { background: var(--bg-hover); color: var(--text); }
.chart { height: 320px; }
.err { color: var(--danger); font-size: 12px; padding: 0 12px 8px; margin: 0; }
.json { width: 100%; height: 360px; font-family: var(--font-mono); font-size: 12.5px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 10px; background: var(--bg-sunken); color: var(--text); outline: none; resize: vertical; }
</style>
