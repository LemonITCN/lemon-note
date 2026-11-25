<script setup lang="ts">
// 页面 / 标注框图标选择：自绘图标 + 颜色，或输入任意字符（可以是表情）。
import { ref, computed } from 'vue'
import { PopoverRoot, PopoverTrigger, PopoverPortal, PopoverContent } from 'reka-ui'
import Icon from '@/components/icons/Icon.vue'
import { COLORS } from '@/components/editor/extensions/marks'

const props = defineProps<{ disabled?: boolean; removable?: boolean }>()
const emit = defineEmits<{ pick: [v: string] }>()
const open = ref(false)
const color = ref('')
const q = ref('')
const custom = ref('')
const names = ['page', 'database', 'star', 'bookmark', 'info', 'warning', 'question', 'success', 'error', 'calendar', 'clock', 'home', 'user', 'users', 'folder', 'file',
  'image', 'video', 'audio', 'code', 'terminal', 'server', 'cloud', 'globe', 'link', 'key', 'lock', 'shield', 'chart', 'flowchart', 'mindmap', 'whiteboard', 'table',
  'list-check', 'tags', 'pin', 'mail', 'phone', 'comment', 'sparkle', 'logo', 'palette', 'formula', 'math', 'history', 'inbox', 'board', 'gallery', 'timeline', 'graph']
const shown = computed(() => names.filter((n) => !q.value || n.includes(q.value.toLowerCase())))
function pick(name: string) { emit('pick', 'icon:' + name + (color.value ? '/' + color.value : '')); open.value = false }
function pickCustom() { if ([...custom.value].length) { emit('pick', [...custom.value.trim()].slice(0, 2).join('')); custom.value = ''; open.value = false } }
</script>

<template>
  <PopoverRoot v-model:open="open">
    <PopoverTrigger as-child :disabled="props.disabled"><slot /></PopoverTrigger>
    <PopoverPortal>
      <PopoverContent class="ip" side="bottom" align="start" :side-offset="6" :collision-padding="10">
        <div class="ip-top">
          <input v-model="q" class="ip-search" :placeholder="$t('common.search')" />
          <button v-if="removable" class="ip-remove" @click="emit('pick', ''); open = false">{{ $t('common.remove') }}</button>
        </div>
        <div class="ip-colors">
          <button class="sw" :class="{ on: !color }" @click="color = ''"><span class="dot def" /></button>
          <button v-for="c in COLORS" :key="c" class="sw" :class="{ on: color === c }" @click="color = c"><span class="dot" :style="{ background: `var(--c-${c})` }" /></button>
        </div>
        <div class="ip-grid">
          <button v-for="n in shown" :key="n" class="ic" :class="color ? 'c-' + color : ''" @click="pick(n)"><Icon :name="n" :size="20" /></button>
        </div>
        <div class="ip-custom">
          <input v-model="custom" maxlength="8" placeholder="Aa" @keydown.enter.prevent="pickCustom" />
          <button @click="pickCustom">{{ $t('common.apply') }}</button>
        </div>
      </PopoverContent>
    </PopoverPortal>
  </PopoverRoot>
</template>

<style>
.ip { width: 312px; background: var(--bg-elev); border-radius: var(--radius-l); box-shadow: var(--shadow-pop); padding: 10px; z-index: 1200; }
.ip-top { display: flex; gap: 6px; }
.ip-search { flex: 1; height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; }
.ip-remove { border: 0; background: none; color: var(--text-2); font-size: 12.5px; border-radius: 4px; padding: 0 6px; }
.ip-remove:hover { background: var(--bg-hover); }
.ip-colors { display: flex; gap: 4px; margin: 8px 0; }
.ip .sw { width: 22px; height: 22px; border-radius: 50%; border: 0; background: none; display: flex; align-items: center; justify-content: center; padding: 0; }
.ip .sw.on { box-shadow: 0 0 0 2px var(--accent); }
.ip .dot { width: 14px; height: 14px; border-radius: 50%; display: block; }
.ip .dot.def { background: var(--text-2); }
.ip-grid { display: grid; grid-template-columns: repeat(8, 1fr); gap: 2px; max-height: 200px; overflow: auto; }
.ip .ic { aspect-ratio: 1; border: 0; background: none; border-radius: 6px; display: flex; align-items: center; justify-content: center; color: var(--text-2); }
.ip .ic:hover { background: var(--bg-hover); }
.ip-custom { display: flex; gap: 6px; margin-top: 8px; border-top: 1px solid var(--border); padding-top: 8px; }
.ip-custom input { flex: 1; height: 28px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; }
.ip-custom button { border: 0; background: var(--bg-hover); border-radius: var(--radius-m); padding: 0 10px; color: var(--text); }
</style>
