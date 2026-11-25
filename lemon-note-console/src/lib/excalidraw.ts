// Excalidraw 是 React 组件：按需加载 React 与 Excalidraw，挂成一个小岛。
let mod: any = null
let react: any = null
let reactDom: any = null

async function load() {
  if (!mod) {
    ;(window as any).EXCALIDRAW_ASSET_PATH = '/excalidraw-assets/'
    ;[react, reactDom, mod] = await Promise.all([import('react'), import('react-dom/client'), import('@excalidraw/excalidraw')])
    await import('@excalidraw/excalidraw/index.css')
  }
  return mod
}

export interface BoardData { elements: any[]; appState?: Record<string, any>; files?: Record<string, any> }

export function emptyBoard(): BoardData { return { elements: [], appState: { viewBackgroundColor: '#ffffff' }, files: {} } }

export async function mountBoard(el: HTMLElement, data: BoardData, opts: { theme: string; lang: string; onChange?: (d: BoardData) => void }) {
  const ex = await load()
  const root = reactDom.createRoot(el)
  let api: any = null
  let last = ''
  root.render(react.createElement(ex.Excalidraw, {
    initialData: { elements: data.elements || [], appState: { ...(data.appState || {}), theme: opts.theme }, files: data.files || {}, scrollToContent: true },
    theme: opts.theme,
    langCode: opts.lang === 'en' ? 'en' : 'zh-CN',
    excalidrawAPI: (a: any) => { api = a },
    onChange: (elements: readonly any[], appState: any, files: any) => {
      if (!opts.onChange) return
      const live = elements.filter((e: any) => !e.isDeleted)
      const sig = live.map((e: any) => e.id + ':' + e.version).join(',')
      if (sig === last) return
      last = sig
      opts.onChange({ elements: live, appState: { viewBackgroundColor: appState.viewBackgroundColor, gridSize: appState.gridSize ?? null }, files })
    },
    UIOptions: { canvasActions: { loadScene: false, saveToActiveFile: false, export: { saveFileToDisk: true } } },
  }))
  return {
    snapshot(): BoardData | null {
      if (!api) return null
      const appState = api.getAppState()
      return { elements: api.getSceneElements().filter((e: any) => !e.isDeleted), appState: { viewBackgroundColor: appState.viewBackgroundColor, gridSize: appState.gridSize ?? null }, files: api.getFiles() }
    },
    unmount() { root.unmount() },
  }
}

export async function boardSVG(data: BoardData, theme: string): Promise<string> {
  const ex = await load()
  if (!data.elements?.length) return ''
  const svg: SVGSVGElement = await ex.exportToSvg({ elements: data.elements, appState: { ...(data.appState || {}), exportBackground: false, exportWithDarkMode: theme === 'dark' }, files: data.files || {} })
  return svg.outerHTML
}
