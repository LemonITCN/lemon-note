import { defineStore } from 'pinia'
import { ref, computed, reactive } from 'vue'
import { api } from '@/api/client'
import type { PageMeta, Space, Role } from '@/api/types'
import { realtime } from '@/lib/realtime'

export interface SpaceEntry { space: Space; role: Role }

export const useWorkspace = defineStore('workspace', () => {
  const spaces = ref<SpaceEntry[]>([])
  const currentSpaceId = ref<string>('')
  const pages = reactive(new Map<string, PageMeta>()) // 所有已加载空间的页面
  const loadedSpaces = new Set<string>()
  const favorites = ref<PageMeta[]>([])
  const shared = ref<PageMeta[]>([])
  const expanded = reactive(new Set<string>(JSON.parse(localStorage.getItem('ln.expanded') || '[]')))

  const currentSpace = computed(() => spaces.value.find((s) => s.space.id === currentSpaceId.value) || spaces.value[0])

  function persistExpanded() {
    try { localStorage.setItem('ln.expanded', JSON.stringify([...expanded].slice(-500))) } catch {}
  }

  async function loadSpaces() {
    const r = await api.get<{ spaces: SpaceEntry[] }>('/api/spaces')
    spaces.value = r.spaces || []
    const saved = localStorage.getItem('ln.space')
    if (!currentSpaceId.value || !r.spaces.some((s) => s.space.id === currentSpaceId.value)) {
      currentSpaceId.value = r.spaces.find((s) => s.space.id === saved)?.space.id || r.spaces[0]?.space.id || ''
    }
  }

  async function loadTree(spaceId: string, force = false) {
    if (!spaceId || (loadedSpaces.has(spaceId) && !force)) return
    const r = await api.get<{ pages: PageMeta[] }>(`/api/spaces/${spaceId}/tree`)
    for (const [id, p] of pages) if (p.space_id === spaceId && !p.database_id) pages.delete(id)
    for (const p of r.pages || []) pages.set(p.id, p)
    loadedSpaces.add(spaceId)
  }

  async function loadSide() {
    const [f, s] = await Promise.all([api.get<{ pages: PageMeta[] }>('/api/favorites'), api.get<{ pages: PageMeta[] }>('/api/shared-with-me')])
    favorites.value = f.pages || []
    shared.value = s.pages || []
  }

  async function init() {
    await loadSpaces()
    await Promise.all([loadTree(currentSpaceId.value), loadSide()])
    realtime.on('page', ({ meta }: { meta: PageMeta }) => upsert(meta))
    realtime.on('page_removed', ({ id }: { id: string }) => pages.delete(id))
    realtime.on('shares', () => { loadSide(); loadSpaces() })
    realtime.on('open', () => {
      // 重连后刷新，避免断线期间漏掉的变化
      for (const sid of loadedSpaces) loadTree(sid, true)
      loadSide()
    })
  }

  function setSpace(id: string) {
    currentSpaceId.value = id
    try { localStorage.setItem('ln.space', id) } catch {}
    loadTree(id)
  }

  function upsert(meta: PageMeta) {
    pages.set(meta.id, meta)
    const fi = favorites.value.findIndex((f) => f.id === meta.id)
    if (fi >= 0) {
      if (meta.trashed_at) favorites.value.splice(fi, 1)
      else favorites.value[fi] = meta
    }
  }

  function children(spaceId: string, parentId: string | undefined) {
    const out: PageMeta[] = []
    for (const p of pages.values()) {
      if (p.space_id === spaceId && (p.parent_id || '') === (parentId || '') && !p.trashed_at && !p.database_id) out.push(p)
    }
    return out.sort((a, b) => (a.position < b.position ? -1 : a.position > b.position ? 1 : a.id < b.id ? -1 : 1))
  }

  function hasChildren(id: string) {
    for (const p of pages.values()) if (p.parent_id === id && !p.trashed_at && !p.database_id) return true
    return false
  }

  function toggleExpanded(id: string, open?: boolean) {
    const want = open ?? !expanded.has(id)
    if (want) expanded.add(id)
    else expanded.delete(id)
    persistExpanded()
  }

  function revealPath(id: string) {
    let p = pages.get(id)
    const seen = new Set<string>()
    while (p?.parent_id && !seen.has(p.parent_id)) {
      seen.add(p.parent_id)
      expanded.add(p.parent_id)
      p = pages.get(p.parent_id)
    }
    persistExpanded()
  }

  async function createPage(opts: { spaceId?: string; parentId?: string; title?: string; afterId?: string; first?: boolean; templateId?: string }) {
    const body = { space_id: opts.spaceId || currentSpaceId.value, parent_id: opts.parentId || '', title: opts.title || '', after_id: opts.afterId || '', first: !!opts.first, template_id: opts.templateId || '' }
    const r = await api.post<{ meta: PageMeta }>('/api/pages', body)
    upsert(r.meta)
    if (opts.parentId) toggleExpanded(opts.parentId, true)
    return r.meta
  }

  async function movePage(id: string, target: { spaceId?: string; parentId?: string; afterId?: string; first?: boolean }) {
    const r = await api.post<{ meta: PageMeta }>(`/api/pages/${id}/move`, { space_id: target.spaceId || '', parent_id: target.parentId || '', after_id: target.afterId || '', first: !!target.first })
    upsert(r.meta)
    return r.meta
  }

  async function trashPage(id: string) {
    await api.del(`/api/pages/${id}`)
    const p = pages.get(id)
    if (p) pages.set(id, { ...p, trashed_at: Date.now(), trash_root: id })
  }

  async function duplicatePage(id: string, withChildren = true) {
    const r = await api.post<{ meta: PageMeta }>(`/api/pages/${id}/duplicate`, { with_children: withChildren, suffix: '' })
    upsert(r.meta)
    return r.meta
  }

  async function setFavorite(id: string, on: boolean) {
    if (on) await api.put(`/api/favorites/${id}`)
    else await api.del(`/api/favorites/${id}`)
    await loadSide()
  }

  function isFavorite(id: string) { return favorites.value.some((f) => f.id === id) }

  function spaceRole(spaceId: string): Role { return spaces.value.find((s) => s.space.id === spaceId)?.role || '' }

  return { spaces, currentSpaceId, currentSpace, pages, favorites, shared, expanded, init, loadSpaces, loadTree, loadSide, setSpace, upsert,
    children, hasChildren, toggleExpanded, revealPath, createPage, movePage, trashPage, duplicatePage, setFavorite, isFavorite, spaceRole }
})
