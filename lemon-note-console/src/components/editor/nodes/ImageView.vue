<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import { takeUpload, fileURL, uploadFile, pickFile, fileSrc } from '@/lib/files'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { pageContext } from '../context'

const props = defineProps(nodeViewProps)
const toast = useToast()
const busy = ref(false)
const urlInput = ref('')
const src = computed(() => fileURL(props.node.attrs.src))
const wrap = ref<HTMLDivElement>()

async function upload(f?: File | null) {
  f = f ?? (await pickFile('image/*'))
  if (!f) return
  busy.value = true
  try {
    const r = await uploadFile(f, pageContext(props.editor)?.pageId)
    props.updateAttributes({ uploadToken: null, src: fileSrc(r.id), alt: r.name })
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

function useUrl() { if (/^https?:\/\//.test(urlInput.value)) props.updateAttributes({ src: urlInput.value.trim() }) }

function startResize(e: PointerEvent, side: number) {
  e.preventDefault()
  const startX = e.clientX
  const startW = (wrap.value?.querySelector('img') as HTMLImageElement)?.offsetWidth || 400
  const max = wrap.value?.parentElement?.clientWidth || 900
  const move = (ev: PointerEvent) => props.updateAttributes({ width: Math.round(Math.max(80, Math.min(max, startW + (ev.clientX - startX) * side * 2))) })
  const up = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', up) }
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}

onMounted(() => {
  const pending = takeUpload(props.node.attrs.uploadToken)
  if (pending) upload(pending)
})
</script>

<template>
  <NodeViewWrapper class="ln-image" :class="['align-' + (props.node.attrs.align || 'center'), { selected: props.selected }]">
    <div v-if="src" ref="wrap" class="frame" contenteditable="false" :style="{ width: props.node.attrs.width ? props.node.attrs.width + 'px' : undefined }">
      <img :src="src" :alt="props.node.attrs.alt" loading="lazy" draggable="false" />
      <template v-if="props.editor.isEditable">
        <span class="handle l" @pointerdown="startResize($event, -1)" />
        <span class="handle r" @pointerdown="startResize($event, 1)" />
      </template>
    </div>
    <div v-else class="empty" contenteditable="false">
      <button class="pick" :disabled="busy || !props.editor.isEditable" @click="upload()"><Icon name="image" :size="18" />{{ busy ? $t('editor.uploading') : $t('editor.image') }}</button>
      <input v-if="props.editor.isEditable" v-model="urlInput" class="url" placeholder="https://" @keydown.enter.prevent="useUrl" />
    </div>
    <input v-if="src && (props.node.attrs.title || props.editor.isEditable)" class="caption" :value="props.node.attrs.title" :readonly="!props.editor.isEditable"
      :placeholder="$t('editor.captionPlaceholder')" @change="props.updateAttributes({ title: ($event.target as HTMLInputElement).value })" />
  </NodeViewWrapper>
</template>

<style scoped>
.ln-image { margin: 6px 0; display: flex; flex-direction: column; }
.align-center { align-items: center; } .align-left { align-items: flex-start; } .align-right { align-items: flex-end; }
.frame { position: relative; max-width: 100%; }
.frame img { display: block; width: 100%; border-radius: 4px; }
.ln-image.selected img { outline: 2px solid var(--accent); outline-offset: 1px; }
.handle { position: absolute; top: 50%; width: 6px; height: 48px; margin-top: -24px; border-radius: 4px; background: rgba(15,15,15,.55); border: 1px solid rgba(255,255,255,.8); cursor: col-resize; opacity: 0; transition: opacity .12s; }
.frame:hover .handle { opacity: 1; }
.handle.l { left: 6px; } .handle.r { right: 6px; }
.empty { display: flex; gap: 8px; width: 100%; background: var(--bg-soft); border-radius: var(--radius-m); padding: 10px; }
.pick { display: inline-flex; align-items: center; gap: 6px; border: 0; background: var(--bg-hover); border-radius: var(--radius-m); padding: 6px 12px; color: var(--text-2); }
.url { flex: 1; border: 1px solid var(--border); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); outline: none; }
.caption { border: 0; background: none; outline: none; text-align: center; color: var(--text-2); font-size: 13.5px; padding: 6px 0 0; width: 100%; }
.align-left .caption { text-align: left; } .align-right .caption { text-align: right; }
</style>
