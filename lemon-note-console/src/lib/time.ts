import { i18n } from '@/i18n'

/** 相对时间（刚刚、3 分钟前、昨天……）。 */
export function formatRelative(ms: number): string {
  const zh = i18n.global.locale.value !== 'en'
  const d = Date.now() - ms
  const m = Math.floor(d / 60000)
  if (m < 1) return zh ? '刚刚' : 'just now'
  if (m < 60) return zh ? `${m} 分钟前` : `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return zh ? `${h} 小时前` : `${h}h ago`
  const days = Math.floor(h / 24)
  if (days < 7) return zh ? `${days} 天前` : `${days}d ago`
  return formatDate(ms)
}

export function formatDate(ms: number, withTime = false): string {
  const d = new Date(ms)
  const pad = (n: number) => String(n).padStart(2, '0')
  const s = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
  return withTime ? `${s} ${pad(d.getHours())}:${pad(d.getMinutes())}` : s
}
