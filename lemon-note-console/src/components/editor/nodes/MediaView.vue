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
const isVideo = computed(() => props.node.type.name === 'video')
const src = computed(() => fileURL(props.node.attrs.src))

async function upload(f?: File | null) {
  f = f ?? (await pickFile(isVideo.value ? 'video/*' : 'audio/*'))
  if (!f) return
  busy.value = true
  try {
    const r = await uploadFile(f, pageContext(props.editor)?.pageId)
    props.updateAttributes({ uploadToken: null, src: fileSrc(r.id), title: r.name })
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
onMounted(() => { const f = takeUpload(props.node.attrs.uploadToken); if (f) upload(f) })
</script>

<template>
  <NodeViewWrapper class="ln-media" :class="{ selected: props.selected }">
    <template v-if="src">
      <video v-if="isVideo" :src="src" controls preload="metadata" contenteditable="false" />
      <audio v-else :src="src" controls preload="metadata" contenteditable="false" />
    </template>
    <div v-else class="empty" contenteditable="false">
      <button class="pick" :disabled="busy || !props.editor.isEditable" @click="upload()"><Icon :name="isVideo ? 'video' : 'audio'" :size="18" />{{ busy ? $t('editor.uploading') : (isVideo ? $t('editor.video') : $t('editor.audio')) }}</button>
      <input v-if="props.editor.isEditable" v-model="urlInput" class="url" placeholder="https://" @keydown.enter.prevent="/^https?:\/\//.test(urlInput) && props.updateAttributes({ src: urlInput.trim() })" />
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-media { margin: 6px 0; }
.ln-media video { width: 100%; border-radius: var(--radius-m); background: #000; }
.ln-media audio { width: 100%; }
.ln-media.selected video, .ln-media.selected audio { outline: 2px solid var(--accent); }
.empty { display: flex; gap: 8px; background: var(--bg-soft); border-radius: var(--radius-m); padding: 10px; }
.pick { display: inline-flex; align-items: center; gap: 6px; border: 0; background: var(--bg-hover); border-radius: var(--radius-m); padding: 6px 12px; color: var(--text-2); }
.url { flex: 1; border: 1px solid var(--border); border-radius: var(--radius-m); padding: 0 10px; background: var(--bg); outline: none; }
</style>
