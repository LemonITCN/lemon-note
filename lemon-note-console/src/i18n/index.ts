import { createI18n } from 'vue-i18n'
import zh from './zh-CN'
import en from './en'
import { ApiError } from '@/api/client'

export type Locale = 'zh-CN' | 'en'

function initialLocale(): Locale {
  try {
    const saved = localStorage.getItem('ln.locale')
    if (saved === 'en' || saved === 'zh-CN') return saved
  } catch {}
  return navigator.language?.toLowerCase().startsWith('zh') ? 'zh-CN' : 'en'
}

export const i18n = createI18n({ legacy: false, locale: initialLocale(), fallbackLocale: 'zh-CN', messages: { 'zh-CN': zh, en } })

export function setLocale(l: string) {
  const loc: Locale = l === 'en' ? 'en' : 'zh-CN'
  i18n.global.locale.value = loc
  document.documentElement.lang = loc
  try { localStorage.setItem('ln.locale', loc) } catch {}
}

/** 把接口错误翻译成当前语言的提示。 */
export function errorText(e: unknown): string {
  const t = i18n.global.t
  if (e instanceof ApiError) {
    const key = 'errors.' + e.code
    if (i18n.global.locale.value === 'en' && i18n.global.te(key)) return t(key)
    return e.message || (i18n.global.te(key) ? t(key) : e.code)
  }
  if (e instanceof Error) return e.message
  return String(e)
}
