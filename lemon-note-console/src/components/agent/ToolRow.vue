<script setup lang="ts">
import { ref, computed } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import { toolLabel } from './toolLabel'

const props = defineProps<{ name: string; title: string; args: any; status: string; text?: string; duration?: number; data?: any }>()
const open = ref(false)
const label = computed(() => toolLabel(props.name, props.args, props.title || props.name))
const icon = computed(() => ({ done: 'success', error: 'warning', denied: 'error', interrupted: 'error' } as Record<string, string>)[props.status] || '')
const edits = computed(() => (props.name === 'edit_page' ? props.args?.edits || [] : []))
const argText = computed(() => {
  if (!props.args) return ''
  if (props.name === 'write_page' || props.name === 'create_pages') return props.args.content || (props.args.pages || []).map((p: any) => `# ${p.title}\n${p.content || ''}`).join('\n\n')
  return JSON.stringify(props.args, null, 2)
})
</script>

<template>
  <div class="tr" :class="status">
    <button class="tr-head" @click="open = !open">
      <span class="st">
        <span v-if="status === 'running' || status === 'preparing'" class="spin" />
        <Icon v-else :name="icon" :size="14" />
      </span>
      <span class="lbl ln-ellipsis">{{ label }}</span>
      <span v-if="duration && duration > 1500" class="dur">{{ (duration / 1000).toFixed(1) }}s</span>
      <Icon :name="open ? 'chevron-up' : 'chevron-down'" :size="13" class="chev" />
    </button>
    <div v-if="open" class="tr-body">
      <template v-if="edits.length">
        <div v-for="(e, i) in edits" :key="i" class="diff">
          <pre class="del">{{ e.old_string }}</pre>
          <pre class="add">{{ e.new_string }}</pre>
        </div>
      </template>
      <pre v-else-if="argText" class="args">{{ argText }}</pre>
      <pre v-if="text" class="res" :class="{ err: status === 'error' || status === 'denied' }">{{ text }}</pre>
    </div>
  </div>
</template>

<style scoped>
.tr { border-radius: 6px; }
.tr-head { display: flex; align-items: center; gap: 6px; width: 100%; border: 0; background: none; padding: 3px 4px; border-radius: 5px; color: var(--text-2); font-size: 12.5px; text-align: left; min-width: 0; }
.tr-head:hover { background: var(--bg-hover); }
.st { width: 14px; height: 14px; display: inline-flex; align-items: center; justify-content: center; flex: none; }
.done .st { color: var(--success); }
.error .st { color: var(--warning); }
.denied .st, .interrupted .st { color: var(--danger); }
.lbl { flex: 1; min-width: 0; }
.dur { color: var(--text-3); font-size: 11.5px; }
.chev { color: var(--text-3); opacity: 0; }
.tr-head:hover .chev { opacity: 1; }
.spin { width: 11px; height: 11px; border-radius: 50%; border: 1.6px solid var(--accent); border-right-color: transparent; animation: sp .7s linear infinite; }
@keyframes sp { to { transform: rotate(360deg); } }
.tr-body { margin: 2px 0 6px 20px; display: flex; flex-direction: column; gap: 4px; }
pre { margin: 0; font-family: var(--font-mono); font-size: 11.5px; line-height: 1.5; white-space: pre-wrap; word-break: break-word; max-height: 260px; overflow: auto; padding: 6px 8px; border-radius: 5px; background: var(--bg-sunken); color: var(--text-2); }
.res.err { color: var(--danger); }
.diff { display: flex; flex-direction: column; gap: 2px; }
.del { background: var(--danger-bg); color: var(--text); text-decoration: line-through; text-decoration-color: rgba(212, 76, 71, .45); }
.add { background: rgba(47, 138, 91, .12); color: var(--text); }
</style>
