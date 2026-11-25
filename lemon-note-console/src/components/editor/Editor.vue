<script setup lang="ts">
import { onBeforeUnmount, ref, watch, shallowRef, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { Editor, EditorContent } from '@tiptap/vue-3'
import { DragHandle } from '@tiptap/extension-drag-handle-vue-3'
import type { BlockDoc } from '@/api/types'
import { buildExtensions } from './extensions'
import { BlockSync, REMOTE_META, type SaveState, type RemoteOp } from './blockSync'
import { slashState } from './extensions/slash'
import { mentionState } from './extensions/mention'
import FloatingList from './FloatingList.vue'
import FormatBar from './FormatBar.vue'
import BlockMenu from './BlockMenu.vue'
import Icon from '@/components/icons/Icon.vue'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'
import { realtime } from '@/lib/realtime'
import '@/styles/editor.css'

const props = defineProps<{ pageId: string; spaceId: string; blocks: BlockDoc[]; editable: boolean; pageTitle: string }>()
const emit = defineEmits<{ state: [s: SaveState]; createSubpage: []; createDatabase: []; ready: [e: Editor] }>()
const { t } = useI18n()
const toast = useToast()
const editor = shallowRef<Editor>()
let sync: BlockSync | null = null
const handlePos = ref<number | null>(null)
const blockMenuOpen = ref(false)
let conflictShown = 0

function create() {
  const e = new Editor({
    extensions: buildExtensions(),
    content: { type: 'doc', content: props.blocks.length ? props.blocks.map((b) => b.data) : [{ type: 'paragraph' }] },
    editable: props.editable,
    editorProps: { attributes: { class: 'ln-editor', spellcheck: 'false' } },
    enableContentCheck: true,
    onContentError: ({ error }) => { console.error('内容不符合结构', error) },
  })
  ;(e.storage as any).lnPage = { pageId: props.pageId, spaceId: props.spaceId }
  sync = new BlockSync(props.pageId, e, props.blocks, {
    onState: (s) => emit('state', s),
    onConflict: () => { if (Date.now() - conflictShown > 10000) { conflictShown = Date.now(); toast.info(t('page.conflict')) } },
    onError: (err: any) => { if (err?.code !== 'network') toast.error(errorText(err)) },
  })
  e.on('update', ({ transaction }) => { if (!transaction.getMeta(REMOTE_META)) sync?.schedule() })
  e.on('lnCreateSubpage' as any, () => emit('createSubpage'))
  e.on('lnCreateDatabase' as any, () => emit('createDatabase'))
  editor.value = e
  emit('ready', e)
}

create()

const offBlocks = realtime.on('blocks', (d: { page_id: string; ops: RemoteOp[] }) => {
  if (d.page_id === props.pageId) sync?.applyRemote(d.ops)
})

watch(() => props.editable, (v) => editor.value?.setEditable(v))

function onBeforeUnload(e: BeforeUnloadEvent) {
  if (sync?.dirty) {
    sync.flush(true)
    e.preventDefault()
  }
}
onMounted(() => window.addEventListener('beforeunload', onBeforeUnload))

onBeforeUnmount(() => {
  window.removeEventListener('beforeunload', onBeforeUnload)
  offBlocks()
  if (sync?.dirty) sync.flush()
  sync?.destroy()
  editor.value?.destroy()
})

function onNodeChange({ pos }: { pos: number }) { if (!blockMenuOpen.value) handlePos.value = pos ?? null }

function addBelow() {
  const e = editor.value
  if (!e || handlePos.value === null) return
  const node = e.state.doc.nodeAt(handlePos.value)
  if (!node) return
  const at = handlePos.value + node.nodeSize
  e.chain().insertContentAt(at, { type: 'paragraph', content: [{ type: 'text', text: '/' }] }).setTextSelection(at + 2).focus().run()
}

defineExpose({ flush: () => sync?.flush(), editor })
</script>

<template>
  <div v-if="editor" class="ln-editor-wrap">
    <DragHandle v-if="editable" :editor="editor" :on-node-change="onNodeChange" class="ln-drag">
      <div class="ln-drag-inner">
        <button class="ln-drag-btn" :title="t('editor.basic')" @click="addBelow"><Icon name="plus" :size="16" /></button>
        <BlockMenu v-model:open="blockMenuOpen" :editor="editor" :pos="handlePos" :page-id="pageId" :page-title="pageTitle">
          <button class="ln-drag-btn grip"><Icon name="grip" :size="16" /></button>
        </BlockMenu>
      </div>
    </DragHandle>
    <EditorContent :editor="editor" />
    <FormatBar v-if="editable" :editor="editor" :page-id="pageId" :page-title="pageTitle" />
    <FloatingList :open="slashState.open" :items="slashState.items" :index="slashState.index" :rect="slashState.rect" :empty="t('editor.noResults')"
      @hover="(i: number) => (slashState.index = i)" @pick="(i: number) => slashState.command?.(slashState.items[i])" />
    <FloatingList :open="mentionState.open" :items="mentionState.items" :index="mentionState.index" :rect="mentionState.rect" :empty="t('editor.noResults')"
      @hover="(i: number) => (mentionState.index = i)" @pick="(i: number) => mentionState.command?.(mentionState.items[i])" />
  </div>
</template>

<style scoped>
.ln-editor-wrap { position: relative; }
.ln-drag-inner { display: flex; align-items: center; gap: 0; padding-right: 2px; }
.ln-drag-btn { width: 22px; height: 24px; border: 0; background: none; color: var(--text-3); display: flex; align-items: center; justify-content: center; border-radius: 4px; padding: 0; }
.ln-drag-btn:hover { background: var(--bg-hover); color: var(--text-2); }
.ln-drag-btn.grip { cursor: grab; width: 18px; }
</style>
