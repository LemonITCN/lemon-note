import { reactive } from 'vue'

const state = reactive({ open: false, title: '', message: '', danger: false, okText: '' })
let resolver: ((v: boolean) => void) | null = null

/** 弹出确认框，返回用户是否确认。 */
export function confirmDialog(opts: { title: string; message?: string; danger?: boolean; okText?: string }): Promise<boolean> {
  resolver?.(false)
  Object.assign(state, { open: true, title: opts.title, message: opts.message || '', danger: !!opts.danger, okText: opts.okText || '' })
  return new Promise((res) => { resolver = res })
}

export function useConfirm() {
  return {
    state,
    resolve(v: boolean) {
      state.open = false
      const r = resolver
      resolver = null
      r?.(v)
    },
  }
}
