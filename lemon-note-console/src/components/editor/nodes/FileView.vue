<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import { takeUpload, fileURL, uploadFile, pickFile, fileSrc, formatSize } from '@/lib/files'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { pageContext } from '../context'

const props = defineProps(nodeViewProps)
const toast = useToast()
const busy = ref(false)
const href = computed(() => fileURL(props.node.attrs.src))

async function upload(f?: File | null) {
  f = f ?? (await pickFile())
  if (!f) return
  busy.value = true
  try {
    const r = await uploadFile(f, pageContext(props.editor)?.pageId)
    props.updateAttributes({ uploadToken: null, src: fileSrc(r.id), name: r.name, size: r.size, mime: r.mime })
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
onMounted(() => { const f = takeUpload(props.node.attrs.uploadToken); if (f) upload(f) })
</script>

<template>
  <NodeViewWrapper class="ln-file" :class="{ selected: props.selected }">
    <a v-if="href" class="card" :href="href + '?download=1'" contenteditable="false" @click.stop>
      <Icon name="paperclip" :size="17" /><span class="name ln-ellipsis">{{ props.node.attrs.name || href }}</span><span class="size">{{ formatSize(props.node.attrs.size) }}</span>
    </a>
    <button v-else class="card empty" contenteditable="false" :disabled="busy || !props.editor.isEditable" @click="upload()">
      <Icon name="paperclip" :size="17" /><span>{{ busy ? $t('editor.uploading') : $t('editor.file') }}</span>
    </button>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-file { margin: 2px 0; }
.card { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 5px; text-decoration: none; color: var(--text); border: 0; background: none; width: 100%; text-align: left; }
.card:hover { background: var(--bg-hover); }
.ln-file.selected .card { background: var(--accent-soft); }
.card.empty { background: var(--bg-soft); color: var(--text-2); }
.name { flex: 1; }
.size { color: var(--text-3); font-size: 12.5px; }
</style>
