<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import Menu from '@/components/ui/Menu.vue'
import { fileURL, uploadFile, pickFile, fileSrc } from '@/lib/files'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const props = defineProps<{ cover: string; editable: boolean }>()
const emit = defineEmits<{ change: [v: string] }>()
const { t } = useI18n()
const toast = useToast()

const gradients = [
  'linear-gradient(120deg, #f6d365 0%, #fda085 100%)', 'linear-gradient(120deg, #a1c4fd 0%, #c2e9fb 100%)', 'linear-gradient(120deg, #d4fc79 0%, #96e6a1 100%)',
  'linear-gradient(120deg, #e0c3fc 0%, #8ec5fc 100%)', 'linear-gradient(120deg, #fbc2eb 0%, #a6c1ee 100%)', 'linear-gradient(135deg, #434343 0%, #000000 100%)',
  'linear-gradient(120deg, #89f7fe 0%, #66a6ff 100%)', 'linear-gradient(120deg, #fdfbfb 0%, #ebedee 100%)',
]

const style = computed(() => {
  const c = props.cover
  if (!c) return null
  if (c.startsWith('gradient:')) return { background: gradients[(parseInt(c.slice(9)) - 1) % gradients.length] || gradients[0] }
  return { backgroundImage: `url("${fileURL(c).replace(/"/g, '')}")`, backgroundSize: 'cover', backgroundPosition: 'center' }
})

async function upload() {
  const f = await pickFile('image/*')
  if (!f) return
  try { const r = await uploadFile(f); emit('change', fileSrc(r.id)) } catch (e) { toast.error(errorText(e)) }
}

const menu = computed(() => [
  ...gradients.map((_, i) => ({ label: `${t('page.changeCover')} ${i + 1}`, icon: 'palette', run: () => emit('change', 'gradient:' + (i + 1)) })),
  { separator: true },
  { label: t('editor.image'), icon: 'upload', run: upload },
  { label: t('page.removeCover'), icon: 'trash', danger: true, run: () => emit('change', '') },
])
</script>

<template>
  <div v-if="style" class="cover" :style="style">
    <Menu v-if="editable" :items="menu" align="end"><button class="cover-btn">{{ t('page.changeCover') }}</button></Menu>
  </div>
</template>

<style scoped>
.cover { height: 30vh; max-height: 280px; min-height: 140px; position: relative; }
.cover-btn { position: absolute; right: 16px; bottom: 12px; border: 0; background: rgba(255,255,255,.85); color: #333; font-size: 12.5px; padding: 4px 10px; border-radius: 5px; opacity: 0; transition: opacity .15s; }
.cover:hover .cover-btn { opacity: 1; }
</style>
