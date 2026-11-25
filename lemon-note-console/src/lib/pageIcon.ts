// 页面图标：空表示默认图标；"icon:<名字>" 表示自绘图标；其余视为用户输入的字符（可以是表情）。
export function parseIcon(icon: string | undefined | null): { kind: 'none' } | { kind: 'svg'; name: string; color?: string } | { kind: 'text'; text: string } | { kind: 'image'; src: string } {
  if (!icon) return { kind: 'none' }
  if (icon.startsWith('icon:')) {
    const [name, color] = icon.slice(5).split('/')
    return { kind: 'svg', name, color }
  }
  if (icon.startsWith('file:') || icon.startsWith('http')) return { kind: 'image', src: icon.startsWith('file:') ? '/api/files/' + icon.slice(5) : icon }
  return { kind: 'text', text: icon }
}
