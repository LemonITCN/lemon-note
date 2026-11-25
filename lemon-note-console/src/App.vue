<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useRouter } from 'vue-router'
import { TooltipProvider } from 'reka-ui'
import Toasts from '@/components/ui/Toasts.vue'
import Confirm from '@/components/ui/Confirm.vue'
import Btn from '@/components/ui/Btn.vue'
import { useSession } from '@/stores/session'
import { onAuthError } from '@/api/client'
import { useUI } from '@/stores/ui'

const s = useSession()
useUI()
const router = useRouter()
const failed = ref(false)

async function retry() {
  failed.value = false
  try {
    await s.load()
    router.replace(router.currentRoute.value.fullPath)
  } catch {
    failed.value = true
  }
}

onMounted(async () => {
  await router.isReady()
  if (!s.boot && router.currentRoute.value.name !== 'share') failed.value = true
})

onAuthError(() => {
  if (s.user) {
    s.user = null
    router.replace({ name: 'login', query: { next: router.currentRoute.value.fullPath } })
  }
})
</script>

<template>
  <TooltipProvider :delay-duration="400">
    <div v-if="failed" class="boot-fail">
      <p>{{ $t('boot.failed') }}</p>
      <Btn @click="retry">{{ $t('boot.retry') }}</Btn>
    </div>
    <RouterView v-else />
    <Toasts />
    <Confirm />
  </TooltipProvider>
</template>

<style scoped>
.boot-fail { height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 12px; color: var(--text-2); }
</style>
