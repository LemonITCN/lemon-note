<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { api } from '@/api/client'
import type { PageMeta } from '@/api/types'
import PageIcon from '@/components/page/PageIcon.vue'
import Btn from '@/components/ui/Btn.vue'
import { useWorkspace } from '@/stores/workspace'
import { useSession } from '@/stores/session'
import { useRouter } from 'vue-router'
import { formatRelative } from '@/lib/time'

const { t } = useI18n()
const ws = useWorkspace()
const s = useSession()
const router = useRouter()
const recent = ref<{ meta: PageMeta; visited_at: number }[]>([])
const greeting = computed(() => {
  const h = new Date().getHours()
  const zh = s.user?.locale !== 'en'
  if (h < 6) return zh ? '夜深了' : 'Good night'
  if (h < 12) return zh ? '早上好' : 'Good morning'
  if (h < 18) return zh ? '下午好' : 'Good afternoon'
  return zh ? '晚上好' : 'Good evening'
})

onMounted(async () => {
  try { recent.value = (await api.get('/api/recent')).pages } catch {}
})

async function newPage() {
  const p = await ws.createPage({})
  router.push(`/p/${p.id}`)
}
</script>

<template>
  <div class="home">
    <h1>{{ greeting }}，{{ s.user?.display_name }}</h1>
    <section>
      <div class="sec-head">
        <span>{{ t('nav.recent') }}</span>
        <Btn size="sm" icon="plus" @click="newPage">{{ t('nav.newPage') }}</Btn>
      </div>
      <div v-if="recent.length" class="cards">
        <RouterLink v-for="r in recent.slice(0, 12)" :key="r.meta.id" :to="`/p/${r.meta.id}`" class="card">
          <PageIcon :icon="r.meta.icon" :kind="r.meta.kind" :size="22" />
          <span class="title ln-ellipsis">{{ r.meta.title || t('common.untitled') }}</span>
          <span class="time">{{ formatRelative(r.visited_at) }}</span>
        </RouterLink>
      </div>
      <p v-else class="empty">{{ t('common.none') }}</p>
    </section>
  </div>
</template>

<style scoped>
.home { max-width: 860px; margin: 0 auto; padding: 72px 32px 40px; }
h1 { font-size: 28px; font-weight: 650; margin: 0 0 36px; letter-spacing: -.2px; }
.sec-head { display: flex; align-items: center; justify-content: space-between; color: var(--text-2); font-size: 12.5px; font-weight: 500; margin-bottom: 10px; }
.cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(180px, 1fr)); gap: 10px; }
.card { display: flex; flex-direction: column; gap: 10px; padding: 14px; border: 1px solid var(--border); border-radius: var(--radius-l); text-decoration: none; color: var(--text); background: var(--bg); transition: background .12s, box-shadow .12s; min-width: 0; }
.card:hover { background: var(--bg-soft); }
.title { font-weight: 500; }
.time { font-size: 12px; color: var(--text-3); }
.empty { color: var(--text-3); }
@media (max-width: 760px) { .home { padding: 32px 16px; } }
</style>
