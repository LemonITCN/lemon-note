<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import type { PageMeta } from '@/api/types'
import { useWorkspace } from '@/stores/workspace'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Menu from '@/components/ui/Menu.vue'
import PageIcon from '@/components/page/PageIcon.vue'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { dragState, REF_MIME } from '@/lib/dnd'
import { confirmDialog } from '@/lib/confirm'

const props = defineProps<{ page: PageMeta; depth: number; canEdit: boolean }>()
const ws = useWorkspace()
const route = useRoute()
const router = useRouter()
const toast = useToast()
const { t } = useI18n()
const open = computed(() => ws.expanded.has(props.page.id))
const kids = computed(() => (open.value ? ws.children(props.page.space_id, props.page.id) : []))
const hasKids = computed(() => ws.hasChildren(props.page.id))
const active = computed(() => route.params.id === props.page.id)
const dropPos = ref<'' | 'before' | 'inside' | 'after'>('')
const menuOpen = ref(false)

const menu = computed(() => [
  { label: ws.isFavorite(props.page.id) ? t('page.unfavorite') : t('page.favorite'), icon: 'star', run: () => ws.setFavorite(props.page.id, !ws.isFavorite(props.page.id)) },
  { label: t('page.copyLink'), icon: 'link', run: () => { navigator.clipboard?.writeText(`${location.origin}/p/${props.page.id}`); toast.success(t('common.copied')) } },
  ...(props.canEdit ? [
    { label: t('page.duplicate'), icon: 'duplicate', run: async () => { try { const m = await ws.duplicatePage(props.page.id); router.push(`/p/${m.id}`) } catch (e) { toast.error(errorText(e)) } } },
    { separator: true },
    { label: t('page.moveToTrash'), icon: 'trash', danger: true, run: trash },
  ] : []),
])

async function trash() {
  const ok = await confirmDialog({ title: t('page.moveToTrash'), message: props.page.title || t('common.untitled'), danger: true, okText: t('page.moveToTrash') })
  if (!ok) return
  try {
    await ws.trashPage(props.page.id)
    if (active.value) router.push('/')
  } catch (e) { toast.error(errorText(e)) }
}

async function addChild() {
  try {
    const p = await ws.createPage({ spaceId: props.page.space_id, parentId: props.page.id })
    router.push(`/p/${p.id}`)
  } catch (e) { toast.error(errorText(e)) }
}

function onDragStart(e: DragEvent) {
  dragState.pageId = props.page.id
  e.dataTransfer!.effectAllowed = 'copyMove'
  e.dataTransfer!.setData(REF_MIME, JSON.stringify({ kind: props.page.kind, id: props.page.id, title: props.page.title, icon: props.page.icon }))
  e.dataTransfer!.setData('text/uri-list', `${location.origin}/p/${props.page.id}`)
  e.dataTransfer!.setData('text/plain', props.page.title || t('common.untitled'))
}

function onDragOver(e: DragEvent) {
  if (!dragState.pageId || dragState.pageId === props.page.id || !props.canEdit) return
  e.preventDefault()
  const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
  const y = e.clientY - r.top
  dropPos.value = y < r.height * 0.28 ? 'before' : y > r.height * 0.72 ? 'after' : 'inside'
}

async function onDrop(e: DragEvent) {
  const id = dragState.pageId
  const pos = dropPos.value
  dropPos.value = ''
  if (!id || id === props.page.id) return
  e.preventDefault()
  try {
    if (pos === 'inside') {
      await ws.movePage(id, { parentId: props.page.id })
      ws.toggleExpanded(props.page.id, true)
    } else {
      const sibs = ws.children(props.page.space_id, props.page.parent_id).filter((p) => p.id !== id)
      const idx = sibs.findIndex((p) => p.id === props.page.id)
      if (pos === 'before') {
        const prev = sibs[idx - 1]
        await ws.movePage(id, { spaceId: props.page.space_id, parentId: props.page.parent_id || '', afterId: prev?.id, first: !prev })
      } else {
        await ws.movePage(id, { spaceId: props.page.space_id, parentId: props.page.parent_id || '', afterId: props.page.id })
      }
    }
  } catch (err) { toast.error(errorText(err)) }
}
</script>

<template>
  <div>
    <div class="row" :class="{ active, [`drop-${dropPos}`]: !!dropPos, 'menu-open': menuOpen }" :style="{ paddingLeft: 6 + depth * 14 + 'px' }"
      draggable="true" @dragstart="onDragStart" @dragend="dragState.pageId = ''" @dragover="onDragOver" @dragleave="dropPos = ''" @drop="onDrop"
      @click="router.push(`/p/${page.id}`)">
      <button class="twisty" :class="{ empty: !hasKids }" @click.stop="ws.toggleExpanded(page.id)">
        <Icon :name="open ? 'chevron-down' : 'chevron-right'" :size="14" />
      </button>
      <PageIcon :icon="page.icon" :kind="page.kind" :size="17" class="icon" />
      <span class="title ln-ellipsis">{{ page.title || $t('common.untitled') }}</span>
      <span class="actions" @click.stop>
        <Menu v-model:open="menuOpen" :items="menu"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
        <IconBtn v-if="canEdit" icon="plus" :size="16" :label="$t('nav.addSubpage')" @click="addChild" />
      </span>
    </div>
    <template v-if="open">
      <TreeNode v-for="c in kids" :key="c.id" :page="c" :depth="depth + 1" :can-edit="canEdit" />
      <div v-if="!kids.length" class="no-kids" :style="{ paddingLeft: 26 + (depth + 1) * 14 + 'px' }">{{ $t('common.none') }}</div>
    </template>
  </div>
</template>

<style scoped>
.row { position: relative; display: flex; align-items: center; gap: 4px; height: var(--row-h); padding-right: 4px; margin: 0 6px; border-radius: 5px; color: var(--text-2); cursor: pointer; user-select: none; }
.row:hover, .row.menu-open { background: var(--bg-hover); }
.row.active { background: var(--bg-active); color: var(--text); font-weight: 500; }
.twisty { width: 20px; height: 20px; display: flex; align-items: center; justify-content: center; border: 0; background: none; color: var(--text-3); border-radius: 4px; padding: 0; flex: none; }
.twisty:hover { background: var(--bg-active); color: var(--text); }
.twisty.empty { opacity: .45; }
.title { flex: 1; min-width: 0; }
.actions { display: none; align-items: center; }
.row:hover .actions, .row.menu-open .actions { display: flex; }
.actions :deep(.icon-btn) { width: 22px; height: 22px; }
.drop-inside { background: var(--accent-soft) !important; box-shadow: inset 0 0 0 1px var(--accent); }
.drop-before::before, .drop-after::after { content: ''; position: absolute; left: 8px; right: 8px; height: 2px; background: var(--accent); border-radius: 2px; }
.drop-before::before { top: -1px; }
.drop-after::after { bottom: -1px; }
.no-kids { font-size: 12.5px; color: var(--text-3); height: 26px; display: flex; align-items: center; }
</style>
