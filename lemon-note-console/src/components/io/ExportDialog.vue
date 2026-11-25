<script setup lang="ts">
// 导出页面：Markdown 或 HTML；含子页面或附件时服务端打成 zip。
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'

const open = defineModel<boolean>('open', { default: false })
const props = defineProps<{ pageId: string; hasChildren?: boolean }>()
const { t } = useI18n()
const format = ref('md')
const children = ref(true)

watch(open, (v) => { if (v) children.value = !!props.hasChildren })

function download() {
  const q = new URLSearchParams({ format: format.value })
  if (children.value) q.set('children', '1')
  const a = document.createElement('a')
  a.href = `/api/pages/${encodeURIComponent(props.pageId)}/export?${q}`
  a.download = ''
  a.rel = 'noopener'
  document.body.appendChild(a)
  a.click()
  a.remove()
  open.value = false
}
</script>

<template>
  <Modal v-model:open="open" :title="t('io.exportTitle')" :width="400">
    <div class="rows">
      <div class="row">
        <span class="name">{{ t('io.format') }}</span>
        <Segmented v-model="format" :options="[{ value: 'md', label: 'Markdown' }, { value: 'html', label: 'HTML' }]" />
      </div>
      <div class="row">
        <span class="name">{{ t('io.includeChildren') }}<InfoTip :text="t('io.exportTip')" :width="300" /></span>
        <Switch v-model="children" />
      </div>
    </div>
    <template #footer>
      <Btn @click="open = false">{{ t('common.cancel') }}</Btn>
      <Btn variant="primary" icon="download" @click="download">{{ t('io.download') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.rows { display: flex; flex-direction: column; gap: 14px; padding-top: 4px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; min-height: 32px; }
.name { display: inline-flex; align-items: center; gap: 4px; color: var(--text); }
</style>
