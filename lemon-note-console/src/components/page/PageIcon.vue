<script setup lang="ts">
import { computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { parseIcon } from '@/lib/pageIcon'
const props = withDefaults(defineProps<{ icon?: string; kind?: string; size?: number }>(), { size: 18 })
const p = computed(() => parseIcon(props.icon))
</script>

<template>
  <span class="pi" :style="{ width: size + 'px', height: size + 'px', fontSize: size * 0.86 + 'px' }">
    <Icon v-if="p.kind === 'none'" :name="kind === 'database' ? 'database' : 'page'" :size="size" />
    <Icon v-else-if="p.kind === 'svg'" :name="p.name" :size="size" :class="p.color ? 'c-' + p.color : ''" />
    <img v-else-if="p.kind === 'image'" :src="p.src" alt="" />
    <span v-else class="txt">{{ p.text }}</span>
  </span>
</template>

<style scoped>
.pi { display: inline-flex; align-items: center; justify-content: center; flex: none; color: var(--text-2); line-height: 1; }
.pi img { width: 100%; height: 100%; object-fit: cover; border-radius: 3px; }
.txt { line-height: 1; }
</style>
