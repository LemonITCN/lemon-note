/** 清洗图表库输出的 SVG：去掉脚本、事件属性、javascript: 链接与外部引用。 */
export function sanitizeSVG(svg: string): string {
  const doc = new DOMParser().parseFromString(svg, 'image/svg+xml')
  const root = doc.documentElement
  if (!root || root.nodeName.toLowerCase() !== 'svg') return ''
  const walk = (el: Element) => {
    const tag = el.tagName.toLowerCase()
    if (tag === 'script' || tag === 'iframe' || tag === 'object' || tag === 'embed') { el.remove(); return }
    for (const attr of Array.from(el.attributes)) {
      const n = attr.name.toLowerCase()
      const v = attr.value.trim().toLowerCase()
      if (n.startsWith('on')) el.removeAttribute(attr.name)
      else if ((n === 'href' || n === 'xlink:href' || n === 'src') && (v.startsWith('javascript:') || v.startsWith('data:text/html'))) el.removeAttribute(attr.name)
    }
    for (const c of Array.from(el.children)) walk(c)
  }
  walk(root)
  return new XMLSerializer().serializeToString(root)
}
