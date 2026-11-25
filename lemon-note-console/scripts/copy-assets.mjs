// 把白板字体复制到 public，部署后完全离线可用（不访问任何 CDN）。
import { cpSync, existsSync, mkdirSync } from 'node:fs'
const src = 'node_modules/@excalidraw/excalidraw/dist/prod/fonts'
const dst = 'public/excalidraw-assets/fonts'
if (existsSync(src)) {
  mkdirSync(dst, { recursive: true })
  cpSync(src, dst, { recursive: true })
}
