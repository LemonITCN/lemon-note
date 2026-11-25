// 浏览器端工具：智能体在服务端运行，需要用户浏览器配合时（打开页面、实际渲染图表检查语法）经 WebSocket 下发。
import type { Router } from 'vue-router'
import { realtime } from './realtime'
import { checkMermaid, renderGraphviz } from './diagrams'

export function installClientTools(router: Router) {
  return realtime.on('client_tool', async (d: { call_id: string; tool: string; args: any }) => {
    let result: any = { ok: true }
    try {
      if (d.tool === 'ui_open') {
        await router.push('/p/' + d.args.page_id)
        if (d.args.text) setTimeout(() => highlight(d.args.text), 600)
      } else if (d.tool === 'render_check') {
        if (d.args.kind === 'mermaid') {
          const err = await checkMermaid(d.args.code)
          result = err ? { ok: false, error: err } : { ok: true }
        } else if (d.args.kind === 'graphviz') {
          await renderGraphviz(d.args.code)
        } else {
          JSON.parse(d.args.code)
        }
      }
    } catch (e: any) {
      result = { ok: false, error: String(e?.message || e) }
    }
    realtime.send('client_tool_result', { call_id: d.call_id, result })
  })
}

function highlight(text: string) {
  const root = document.querySelector('.ln-editor')
  if (!root) return
  const walker = document.createTreeWalker(root, NodeFilter.SHOW_TEXT)
  let n: Node | null
  while ((n = walker.nextNode())) {
    const i = (n.textContent || '').indexOf(text.slice(0, 60))
    if (i >= 0) {
      const el = n.parentElement
      el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
      el?.animate([{ background: 'var(--accent-soft)' }, { background: 'transparent' }], { duration: 2400 })
      return
    }
  }
}
