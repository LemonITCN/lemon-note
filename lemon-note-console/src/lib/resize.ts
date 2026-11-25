/** 拖动调整宽度：返回 pointerdown 处理器。 */
export function startResize(e: PointerEvent, get: () => number, set: (w: number) => void, min: number, max: number, dir: 1 | -1 = 1) {
  e.preventDefault()
  const startX = e.clientX
  const startW = get()
  const move = (ev: PointerEvent) => set(Math.max(min, Math.min(max, startW + (ev.clientX - startX) * dir)))
  const up = () => {
    window.removeEventListener('pointermove', move)
    window.removeEventListener('pointerup', up)
    document.body.style.cursor = ''
    document.body.style.userSelect = ''
  }
  document.body.style.cursor = 'col-resize'
  document.body.style.userSelect = 'none'
  window.addEventListener('pointermove', move)
  window.addEventListener('pointerup', up)
}
