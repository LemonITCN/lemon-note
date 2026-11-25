/** 键排序后的 JSON 串（与服务端 Go 的 map 序列化顺序一致），用于比较块内容是否变化。 */
export function canonical(v: unknown): string {
  if (v === null || typeof v !== 'object') return JSON.stringify(v ?? null)
  if (Array.isArray(v)) return '[' + v.map(canonical).join(',') + ']'
  const keys = Object.keys(v as Record<string, unknown>).filter((k) => (v as any)[k] !== undefined).sort()
  return '{' + keys.map((k) => JSON.stringify(k) + ':' + canonical((v as any)[k])).join(',') + '}'
}
