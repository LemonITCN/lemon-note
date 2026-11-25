import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from './stores/session'
import { pub, enterPublic, leavePublic } from './lib/publicCtx'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/setup', name: 'setup', component: () => import('./views/SetupView.vue') },
    { path: '/share/:token/:pageId?', name: 'share', component: () => import('./views/PublicView.vue'), props: true },
    { path: '/login', name: 'login', component: () => import('./views/LoginView.vue') },
    { path: '/register', name: 'register', component: () => import('./views/LoginView.vue'), props: { register: true } },
    {
      path: '/',
      component: () => import('./views/AppShell.vue'),
      children: [
        { path: '', name: 'home', component: () => import('./views/HomeView.vue') },
        { path: 'p/:id', name: 'page', component: () => import('./views/PageRoute.vue'), props: true },
      ],
    },
    { path: '/:pathMatch(.*)*', redirect: '/' },
  ],
})

router.beforeEach(async (to) => {
  // 公开分享页：不需要登录；页面里的站内跳转（/p/<id>）留在分享范围内
  if (to.name === 'share') { enterPublic(String(to.params.token)); return true }
  if (pub.token && to.name === 'page') return { name: 'share', params: { token: pub.token, pageId: String(to.params.id) } }
  if (pub.token) leavePublic()
  const s = useSession()
  if (!s.boot) {
    try { await s.load() } catch { return true }
  }
  if (s.boot?.mode === 'setup') return to.name === 'setup' ? true : { name: 'setup', query: to.query }
  if (to.name === 'setup') return { name: 'login' }
  const open = to.name === 'login' || to.name === 'register'
  if (!s.user && !open) return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  if (s.user && open) return { name: 'home' }
  return true
})
