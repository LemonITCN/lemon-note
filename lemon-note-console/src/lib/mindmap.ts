// 思维导图数据：我们存的格式 { root: { id, text, children, style, note }, layout, theme }
// 与 simple-mind-map 的 { data: { text, uid, note, ...样式 }, children } 互转。
export interface MindNode { id?: string; text: string; children?: MindNode[]; style?: Record<string, any>; note?: string; collapsed?: boolean }
export interface MindData { root: MindNode; layout?: string; theme?: string }

export function defaultMind(text = '中心主题'): MindData {
  return { root: { text, children: [{ text: '分支一' }, { text: '分支二' }] }, layout: 'logicalStructure', theme: 'default' }
}

export function toSMM(n: MindNode): any {
  return {
    data: { text: n.text, ...(n.id ? { uid: n.id } : {}), ...(n.note ? { note: n.note } : {}), ...(n.collapsed ? { expand: false } : {}), ...(n.style || {}) },
    children: (n.children || []).map(toSMM),
  }
}

const reserved = new Set(['text', 'uid', 'note', 'expand', 'isActive', 'richText', 'imgMap'])

export function fromSMM(n: any): MindNode {
  const d = n?.data || {}
  const style: Record<string, any> = {}
  for (const [k, v] of Object.entries(d)) if (!reserved.has(k) && v !== undefined && v !== null && v !== '') style[k] = v
  const out: MindNode = { text: String(d.text ?? '') }
  if (d.uid) out.id = d.uid
  if (d.note) out.note = d.note
  if (d.expand === false) out.collapsed = true
  if (Object.keys(style).length) out.style = style
  const kids = (n?.children || []).map(fromSMM)
  if (kids.length) out.children = kids
  return out
}

export function mindToOutline(n: MindNode, depth = 0): string {
  return '  '.repeat(depth) + '- ' + n.text + '\n' + (n.children || []).map((c) => mindToOutline(c, depth + 1)).join('')
}
