<script setup lang="ts">
import { watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Section from './Section.vue'
import Row from './Row.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { useUI } from '@/stores/ui'
import { useSession } from '@/stores/session'

const { t } = useI18n()
const ui = useUI()
const s = useSession()
const accents = ['blue', 'green', 'purple', 'orange', 'pink', 'lemon', 'graphite']
const swatch: Record<string, string> = { blue: '#2f6fde', green: '#2f8a5b', purple: '#7b5bd6', orange: '#d9730d', pink: '#c94f8b', lemon: '#b59a12', graphite: '#4a4a48' }
let timer: number | undefined
watch(() => ({ ...ui.appearance }), (v) => {
  clearTimeout(timer)
  timer = window.setTimeout(() => s.savePrefs({ appearance: v }).catch(() => {}), 500)
}, { deep: true })
</script>

<template>
  <Section :title="t('settings.appearance')">
    <Row :label="t('settings.theme')">
      <Segmented v-model="ui.appearance.theme" :options="[{ value: 'light', label: t('settings.themeLight'), icon: 'sun' }, { value: 'dark', label: t('settings.themeDark'), icon: 'moon' }, { value: 'system', label: t('settings.themeSystem'), icon: 'monitor' }]" />
    </Row>
    <Row :label="t('settings.accent')">
      <div class="sw">
        <button v-for="a in accents" :key="a" class="dot" :class="{ on: ui.appearance.accent === a }" :style="{ background: swatch[a] }" :title="a" @click="ui.appearance.accent = a" />
      </div>
    </Row>
    <Row :label="t('settings.font')">
      <Segmented v-model="ui.appearance.font" :options="[{ value: 'default', label: t('page.fontDefault') }, { value: 'serif', label: t('page.fontSerif') }, { value: 'mono', label: t('page.fontMono') }]" />
    </Row>
    <Row :label="t('settings.density')">
      <Segmented v-model="ui.appearance.density" :options="[{ value: 'compact', label: t('settings.densityCompact') }, { value: 'default', label: t('settings.densityDefault') }, { value: 'comfortable', label: t('settings.densityComfortable') }]" />
    </Row>
  </Section>
</template>

<style scoped>
.sw { display: flex; gap: 8px; }
.dot { width: 22px; height: 22px; border-radius: 50%; border: 2px solid transparent; box-shadow: 0 0 0 1px var(--border); }
.dot.on { box-shadow: 0 0 0 2px var(--bg-elev), 0 0 0 4px var(--text-2); }
</style>
