import { createRouter, createWebHistory } from 'vue-router'
import { useSession } from './stores/session'

export const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/setup', name: 'setup', component: () => import('./views/SetupView.vue') },
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
  const s = useSession()
  if (!s.boot) {
    try { await s.load() } catch { return true }
  }
  if (s.boot?.mode === 'setup') return to.name === 'setup' ? true : { name: 'setup', query: to.query }
  if (to.name === 'setup') return { name: 'login' }
  const pub = to.name === 'login' || to.name === 'register'
  if (!s.user && !pub) return { name: 'login', query: to.fullPath !== '/' ? { next: to.fullPath } : {} }
  if (s.user && pub) return { name: 'home' }
  return true
})
