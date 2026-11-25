<script setup lang="ts">
// 数据库视图（完整实现见数据库模块）。
defineProps<{ databaseId: string; editable?: boolean; full?: boolean; viewId?: string }>()
</script>

<template>
  <div class="db-view" />
</template>
