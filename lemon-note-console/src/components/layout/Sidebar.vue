<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Menu from '@/components/ui/Menu.vue'
import TreeNode from './TreeNode.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import TrashPanel from '@/components/page/TrashPanel.vue'
import { useWorkspace } from '@/stores/workspace'
import { useUI } from '@/stores/ui'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { roleAtLeast } from '@/api/types'
import { dragState } from '@/lib/dnd'
import NewSpaceDialog from './NewSpaceDialog.vue'
import { dbApi } from '@/lib/db'

const { t } = useI18n()
const ws = useWorkspace()
const ui = useUI()
const router = useRouter()
const toast = useToast()
const trashOpen = ref(false)
const newSpaceOpen = ref(false)
const rootDrop = ref(false)
const roots = computed(() => (ws.currentSpace ? ws.children(ws.currentSpace.space.id, undefined) : []))
const canEdit = computed(() => roleAtLeast(ws.currentSpace?.role || '', 'editor'))
const showFav = ref(true)
const showShared = ref(true)

const spaceMenu = computed(() => [
  ...ws.spaces.map((s) => ({ label: s.space.name, icon: s.space.kind === 'personal' ? 'user' : 'users', checked: s.space.id === ws.currentSpaceId, run: () => ws.setSpace(s.space.id) })),
  { separator: true },
  { label: t('nav.newSpace'), icon: 'plus', run: () => (newSpaceOpen.value = true) },
  { label: t('settings.spaces'), icon: 'settings', run: () => ui.openSettings('spaces') },
])

const newMenu = computed(() => [
  { label: t('nav.newPage'), icon: 'page', run: newPage },
  { label: t('editor.database'), icon: 'database', run: newDatabase },
])

async function newDatabase() {
  try {
    const r = await dbApi.create({ space_id: ws.currentSpace!.space.id, title: '' })
    ws.upsert({ ...r.page, parent_id: undefined, database_id: undefined } as any)
    router.push(`/p/${r.page.id}`)
  } catch (e) { toast.error(errorText(e)) }
}

async function newPage() {
  try {
    const p = await ws.createPage({})
    router.push(`/p/${p.id}`)
    if (ui.isMobile) ui.mobileSidebar = false
  } catch (e) { toast.error(errorText(e)) }
}

async function dropToRoot(e: DragEvent) {
  rootDrop.value = false
  const id = dragState.pageId
  if (!id || !ws.currentSpace) return
  e.preventDefault()
  try {
    const last = roots.value.filter((p) => p.id !== id).at(-1)
    await ws.movePage(id, { spaceId: ws.currentSpace.space.id, parentId: '', afterId: last?.id })
  } catch (err) { toast.error(errorText(err)) }
}
</script>

<template>
  <aside class="sidebar">
    <div class="top">
      <Menu :items="spaceMenu" :width="240">
        <button class="space-btn">
          <span class="space-avatar">{{ (ws.currentSpace?.space.name || '?').slice(0, 1) }}</span>
          <span class="ln-ellipsis">{{ ws.currentSpace?.space.name }}</span>
          <Icon name="chevron-down" :size="14" />
        </button>
      </Menu>
      <Menu v-if="canEdit" :items="newMenu" align="end"><IconBtn icon="new-page" :label="t('nav.newPage')" /></Menu>
    </div>
    <div class="scroll">
      <div class="quick">
        <button class="item" @click="ui.searchOpen = true"><Icon name="search" :size="17" /><span>{{ t('nav.search') }}</span></button>
        <RouterLink to="/" class="item"><Icon name="home" :size="17" /><span>{{ t('nav.home') }}</span></RouterLink>
      </div>
      <template v-if="ws.favorites.length">
        <button class="sec" @click="showFav = !showFav">{{ t('nav.favorites') }}<Icon :name="showFav ? 'chevron-down' : 'chevron-right'" :size="12" /></button>
        <template v-if="showFav">
          <RouterLink v-for="p in ws.favorites" :key="p.id" :to="`/p/${p.id}`" class="item fav">
            <PageIcon :icon="p.icon" :kind="p.kind" :size="17" /><span class="ln-ellipsis">{{ p.title || t('common.untitled') }}</span>
          </RouterLink>
        </template>
      </template>
      <template v-if="ws.shared.length">
        <button class="sec" @click="showShared = !showShared">{{ t('nav.shared') }}<Icon :name="showShared ? 'chevron-down' : 'chevron-right'" :size="12" /></button>
        <template v-if="showShared">
          <RouterLink v-for="p in ws.shared" :key="p.id" :to="`/p/${p.id}`" class="item fav">
            <PageIcon :icon="p.icon" :kind="p.kind" :size="17" /><span class="ln-ellipsis">{{ p.title || t('common.untitled') }}</span>
          </RouterLink>
        </template>
      </template>
      <div class="sec static" :class="{ droppable: rootDrop }" @dragover.prevent="rootDrop = !!dragState.pageId" @dragleave="rootDrop = false" @drop="dropToRoot">
        {{ t('nav.pages') }}
        <IconBtn v-if="canEdit" class="sec-add" icon="plus" :size="14" :label="t('nav.newPage')" @click="newPage" />
      </div>
      <TreeNode v-for="p in roots" :key="p.id" :page="p" :depth="0" :can-edit="canEdit" />
      <button v-if="canEdit && !roots.length" class="item faint" @click="newPage"><Icon name="plus" :size="16" /><span>{{ t('nav.newPage') }}</span></button>
    </div>
    <div class="bottom">
      <button class="item" @click="trashOpen = true"><Icon name="trash" :size="17" /><span>{{ t('nav.trash') }}</span></button>
    </div>
    <TrashPanel v-model:open="trashOpen" />
    <NewSpaceDialog v-model:open="newSpaceOpen" />
  </aside>
</template>

<style scoped>
.sidebar { height: 100%; display: flex; flex-direction: column; background: var(--bg-soft); min-width: 0; }
.top { display: flex; align-items: center; gap: 4px; padding: 10px 10px 6px 8px; }
.space-btn { flex: 1; min-width: 0; display: flex; align-items: center; gap: 8px; border: 0; background: none; height: 32px; padding: 0 6px; border-radius: 6px; font-weight: 600; color: var(--text); text-align: left; }
.space-btn:hover { background: var(--bg-hover); }
.space-avatar { width: 22px; height: 22px; border-radius: 5px; background: var(--accent); color: #fff; display: flex; align-items: center; justify-content: center; font-size: 12px; flex: none; }
.scroll { flex: 1; overflow-y: auto; padding-bottom: 12px; }
.quick { padding: 2px 0 8px; }
.item { display: flex; align-items: center; gap: 8px; height: var(--row-h); padding: 0 8px; margin: 0 6px; border-radius: 5px; color: var(--text-2); border: 0; background: none; width: calc(100% - 12px); text-align: left; text-decoration: none; }
.item:hover { background: var(--bg-hover); color: var(--text); }
.item.router-link-exact-active { background: var(--bg-active); color: var(--text); }
.item.fav { padding-left: 10px; }
.item.faint { color: var(--text-3); }
.sec { display: flex; align-items: center; gap: 4px; width: 100%; border: 0; background: none; font-size: 12px; font-weight: 500; color: var(--text-3); padding: 12px 14px 4px; text-align: left; }
.sec.static { justify-content: space-between; padding-right: 10px; }
.sec.droppable { color: var(--accent); }
.sec-add { width: 20px !important; height: 20px !important; }
.bottom { border-top: 1px solid var(--border); padding: 6px 0 8px; }
</style>
