<script setup lang="ts">
// 内嵌数据库（完整实现见数据库模块）。
defineProps<{ databaseId: string; viewId?: string; editable?: boolean }>()
</script>

<template>
  <div class="db-embed" contenteditable="false"><RouterLink :to="`/p/${databaseId}`">{{ $t('editor.database') }}</RouterLink></div>
</template>

<style scoped>
.db-embed { padding: 8px 0; }
</style>
