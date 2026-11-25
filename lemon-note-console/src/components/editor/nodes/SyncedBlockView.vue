<script setup lang="ts">
import { NodeViewWrapper, NodeViewContent, nodeViewProps } from '@tiptap/vue-3'
const props = defineProps(nodeViewProps)
</script>

<template>
  <NodeViewWrapper class="ln-synced" :data-source="props.node.attrs.sourceId || ''">
    <NodeViewContent />
  </NodeViewWrapper>
</template>
