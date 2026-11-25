<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { NodeViewWrapper, nodeViewProps } from '@tiptap/vue-3'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'

const props = defineProps(nodeViewProps)
const url = ref('')
const busy = ref(false)

async function fetchMeta(u: string) {
  busy.value = true
  props.updateAttributes({ url: u })
  try {
    const r = await api.post('/api/unfurl', { url: u })
    props.updateAttributes({ title: r.title || '', description: r.description || '', image: r.image || '' })
  } catch {} finally { busy.value = false }
}

function host(u: string) { try { return new URL(u).host } catch { return u } }
onMounted(() => { if (props.node.attrs.url && !props.node.attrs.title && props.editor.isEditable) fetchMeta(props.node.attrs.url) })
</script>

<template>
  <NodeViewWrapper class="ln-bookmark" :class="{ selected: props.selected }">
    <a v-if="props.node.attrs.url" class="card" :href="props.node.attrs.url" target="_blank" rel="noopener noreferrer nofollow" contenteditable="false">
      <div class="text">
        <div class="t ln-ellipsis">{{ props.node.attrs.title || host(props.node.attrs.url) }}</div>
        <div v-if="props.node.attrs.description" class="d">{{ props.node.attrs.description }}</div>
        <div class="u ln-ellipsis"><Icon name="globe" :size="13" />{{ props.node.attrs.url }}</div>
      </div>
      <div v-if="props.node.attrs.image" class="img"><img :src="props.node.attrs.image" alt="" referrerpolicy="no-referrer" loading="lazy" /></div>
    </a>
    <div v-else class="empty" contenteditable="false">
      <Icon name="bookmark" :size="17" />
      <input v-model="url" :placeholder="$t('editor.linkPlaceholder')" :disabled="!props.editor.isEditable" @keydown.enter.prevent="/^https?:\/\//.test(url) && fetchMeta(url.trim())" />
    </div>
  </NodeViewWrapper>
</template>

<style scoped>
.ln-bookmark { margin: 6px 0; }
.card { display: flex; border: 1px solid var(--border); border-radius: var(--radius-m); overflow: hidden; text-decoration: none; color: var(--text); min-height: 96px; }
.card:hover { background: var(--bg-soft); }
.ln-bookmark.selected .card { border-color: var(--accent); }
.text { flex: 1; min-width: 0; padding: 12px 14px; display: flex; flex-direction: column; gap: 4px; }
.t { font-weight: 500; }
.d { font-size: 12.5px; color: var(--text-2); display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
.u { font-size: 12px; color: var(--text-3); display: flex; align-items: center; gap: 4px; margin-top: auto; }
.img { width: 30%; max-width: 220px; flex: none; }
.img img { width: 100%; height: 100%; object-fit: cover; }
.empty { display: flex; align-items: center; gap: 8px; background: var(--bg-soft); border-radius: var(--radius-m); padding: 8px 12px; color: var(--text-3); }
.empty input { flex: 1; border: 0; background: none; outline: none; }
</style>
