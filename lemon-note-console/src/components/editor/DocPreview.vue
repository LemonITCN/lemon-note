<script setup lang="ts">
// 只读渲染一组块（历史版本、公开分享页用）。
import { onBeforeUnmount, shallowRef, watch } from 'vue'
import { Editor, EditorContent } from '@tiptap/vue-3'
import { buildExtensions } from './extensions'
import '@/styles/editor.css'

const props = defineProps<{ blocks: any[]; pageId?: string; spaceId?: string }>()
const editor = shallowRef<Editor>()

function make() {
  editor.value?.destroy()
  const e = new Editor({ extensions: buildExtensions({ placeholder: false }), editable: false,
    content: { type: 'doc', content: props.blocks.length ? props.blocks : [{ type: 'paragraph' }] }, editorProps: { attributes: { class: 'ln-editor' } } })
  ;(e.storage as any).lnPage = { pageId: props.pageId || '', spaceId: props.spaceId || '' }
  editor.value = e
}
make()
watch(() => props.blocks, make)
onBeforeUnmount(() => editor.value?.destroy())
</script>

<template>
  <EditorContent v-if="editor" :editor="editor" />
</template>
