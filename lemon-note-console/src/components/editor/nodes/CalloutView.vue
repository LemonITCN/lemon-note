<script setup lang="ts">
import { NodeViewWrapper, NodeViewContent, nodeViewProps } from '@tiptap/vue-3'
import PageIcon from '@/components/page/PageIcon.vue'
import IconPicker from '@/components/page/IconPicker.vue'
const props = defineProps(nodeViewProps)
</script>

<template>
  <NodeViewWrapper class="ln-callout" :class="props.node.attrs.color ? 'cb-' + String(props.node.attrs.color).replace('_background', '') : 'cb-gray'">
    <IconPicker :disabled="!props.editor.isEditable" @pick="(v: string) => props.updateAttributes({ icon: v || 'icon:info' })">
      <button class="ln-callout-icon" contenteditable="false"><PageIcon :icon="props.node.attrs.icon" :size="20" /></button>
    </IconPicker>
    <NodeViewContent class="ln-callout-body" />
  </NodeViewWrapper>
</template>
