<script setup lang="ts">
import IconBtn from '@/components/ui/IconBtn.vue'
import { useUI } from '@/stores/ui'
const ui = useUI()
</script>

<template>
  <div class="agent">
    <div class="head"><span class="title">{{ $t('nav.assistant') }}</span><span class="ln-spacer" /><IconBtn icon="close" :label="$t('common.close')" @click="ui.toggleAgent(false)" /></div>
    <div class="body" />
  </div>
</template>

<style scoped>
.agent { height: 100%; display: flex; flex-direction: column; }
.head { display: flex; align-items: center; height: 44px; padding: 0 8px 0 14px; border-bottom: 1px solid var(--border); }
.title { font-weight: 600; }
.body { flex: 1; }
</style>
