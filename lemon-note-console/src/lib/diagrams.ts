// 图表渲染：Mermaid 与 Graphviz 都按需加载，首次使用时才下载。
let mermaidMod: any = null
let mermaidTheme = ''
let gv: any = null
let mermaidSeq = 0

export async function renderMermaid(code: string, theme: string): Promise<string> {
  if (!mermaidMod) mermaidMod = (await import('mermaid')).default
  if (mermaidTheme !== theme) {
    mermaidMod.initialize({ startOnLoad: false, securityLevel: 'strict', theme: theme === 'dark' ? 'dark' : 'neutral', fontFamily: 'inherit' })
    mermaidTheme = theme
  }
  await mermaidMod.parse(code)
  const id = 'ln-mmd-' + ++mermaidSeq
  const { svg } = await mermaidMod.render(id, code)
  document.getElementById(id)?.remove()
  return svg
}

/** 只做语法检查，返回错误信息（没有错误返回空串）。 */
export async function checkMermaid(code: string): Promise<string> {
  try {
    if (!mermaidMod) mermaidMod = (await import('mermaid')).default
    await mermaidMod.parse(code)
    return ''
  } catch (e: any) { return String(e?.message || e) }
}

export async function renderGraphviz(code: string, engine = 'dot'): Promise<string> {
  if (!gv) {
    const { Graphviz } = await import('@hpcc-js/wasm-graphviz')
    gv = await Graphviz.load()
  }
  return gv.layout(code, 'svg', engine)
}
