import { defineStore } from 'pinia'
import { ref } from 'vue'

export interface Toast { id: number; kind: 'info' | 'success' | 'error'; text: string; action?: { label: string; run: () => void } }

let seq = 0

export const useToast = defineStore('toast', () => {
  const items = ref<Toast[]>([])
  function push(kind: Toast['kind'], text: string, action?: Toast['action'], ms = 3500) {
    const t = { id: ++seq, kind, text, action }
    items.value.push(t)
    setTimeout(() => dismiss(t.id), action ? ms * 2 : ms)
  }
  function dismiss(id: number) { items.value = items.value.filter((x) => x.id !== id) }
  return {
    items, dismiss,
    info: (t: string, a?: Toast['action']) => push('info', t, a),
    success: (t: string) => push('success', t),
    error: (t: string) => push('error', t, undefined, 5000),
  }
})
