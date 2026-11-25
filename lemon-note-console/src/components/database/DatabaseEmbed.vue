<script setup lang="ts">
// 页面正文里的内嵌数据库块。
import DatabaseView from './DatabaseView.vue'
defineProps<{ databaseId: string; viewId?: string; editable?: boolean }>()
</script>

<template>
  <div class="db-embed" contenteditable="false" @mousedown.stop @keydown.stop>
    <DatabaseView v-if="databaseId" :database-id="databaseId" :view-id="viewId" :editable="editable" inline-title />
  </div>
</template>

<style scoped>
.db-embed { padding: 4px 0; font-family: var(--font-ui); font-size: var(--ui-size); }
</style>
