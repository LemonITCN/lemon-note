<script setup lang="ts">
import { NodeViewWrapper, NodeViewContent, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
const props = defineProps(nodeViewProps)
function toggle() { props.updateAttributes({ open: !props.node.attrs.open }) }
</script>

<template>
  <NodeViewWrapper class="ln-toggle" :class="[{ closed: !props.node.attrs.open }, props.node.attrs.level ? 'h' + props.node.attrs.level : '']">
    <button class="ln-toggle-btn" contenteditable="false" @click="toggle"><Icon :name="props.node.attrs.open ? 'chevron-down' : 'chevron-right'" :size="16" /></button>
    <NodeViewContent class="ln-toggle-content" />
  </NodeViewWrapper>
</template>
