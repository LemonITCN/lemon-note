// 编辑器与服务器之间的块级同步：
// 本地改动 400ms 后按块 id 比对出 put / del 操作提交；远端改动按块打补丁，不打断本地光标与撤销栈。
import type { Editor } from '@tiptap/core'
import type { Node as PMNode } from '@tiptap/pm/model'
import { api, getConnId } from '@/api/client'
import type { BlockDoc } from '@/api/types'
import { between, lis } from '@/lib/fracidx'
import { canonical } from '@/lib/canonical'

interface Known { version: number; position: string; json: string }
export interface RemoteOp { op: 'put' | 'del'; id: string; type?: string; data?: any; position?: string; version?: number }
export type SaveState = 'saved' | 'saving' | 'error' | 'offline'

export interface SyncHooks {
  onState?: (s: SaveState) => void
  onConflict?: () => void
  onError?: (e: unknown) => void
}

export const REMOTE_META = 'lnRemote'

export class BlockSync {
  private known = new Map<string, Known>()
  private timer: number | undefined
  private inflight: Promise<void> | null = null
  private pending = false
  private destroyed = false

  constructor(private pageId: string, private editor: Editor, initial: BlockDoc[], private hooks: SyncHooks = {}) {
    for (const b of initial) this.known.set(b.id, { version: b.version, position: b.position, json: canonical(b.data) })
  }

  /** 本地内容变化后调用。 */
  schedule() {
    if (this.destroyed) return
    this.pending = true
    this.hooks.onState?.('saving')
    clearTimeout(this.timer)
    this.timer = window.setTimeout(() => this.flush(), 400)
  }

  get dirty() { return this.pending || !!this.inflight }

  private topNodes(): PMNode[] {
    const out: PMNode[] = []
    this.editor.state.doc.forEach((n) => out.push(n))
    return out
  }

  private computeOps() {
    const nodes = this.topNodes()
    const ops: any[] = []
    const seen = new Set<string>()
    // 保持相对顺序不变的已有块沿用原位置
    const idxs: number[] = []
    const poss: string[] = []
    nodes.forEach((n, i) => {
      const k = this.known.get(n.attrs.id)
      if (k && !seen.has(n.attrs.id)) { idxs.push(i); poss.push(k.position) }
      seen.add(n.attrs.id)
    })
    const keep = new Set(lis(poss).map((k) => idxs[k]))
    seen.clear()
    let prev: string | null = null
    const positions: string[] = []
    for (let i = 0; i < nodes.length; i++) {
      const id = nodes[i].attrs.id as string
      let pos: string
      if (keep.has(i) && !seen.has(id)) pos = this.known.get(id)!.position
      else {
        let next: string | null = null
        for (let j = i + 1; j < nodes.length; j++) if (keep.has(j)) { next = this.known.get(nodes[j].attrs.id)!.position; break }
        try { pos = between(prev, next) } catch { pos = between(prev, null) }
      }
      positions.push(pos)
      prev = pos
      seen.add(id)
    }
    const present = new Set<string>()
    nodes.forEach((n, i) => {
      const id = n.attrs.id as string
      if (!id || present.has(id)) return
      present.add(id)
      const json = n.toJSON()
      const cj = canonical(json)
      const k = this.known.get(id)
      if (k && k.json === cj && k.position === positions[i]) return
      ops.push({ op: 'put', id, type: n.type.name, data: json, position: positions[i], base_version: k?.version || 0, _json: cj })
    })
    for (const [id, k] of this.known) if (!present.has(id)) ops.push({ op: 'del', id, base_version: k.version })
    return ops
  }

  /** 立即提交（keepalive 用于离开页面时）。 */
  async flush(keepalive = false): Promise<void> {
    clearTimeout(this.timer)
    if (this.inflight) {
      await this.inflight
      if (this.pending) return this.flush(keepalive)
      return
    }
    if (!this.pending) return
    this.pending = false
    const ops = this.computeOps()
    if (!ops.length) { this.hooks.onState?.('saved'); return }
    const body = { ops: ops.map(({ _json, ...o }) => o) }
    this.inflight = (async () => {
      try {
        let res: any
        if (keepalive) {
          await fetch(`/api/pages/${this.pageId}/ops`, { method: 'POST', keepalive: true, credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': (await import('@/api/client')).getCsrf(), 'X-Lemon-Conn': getConnId() }, body: JSON.stringify(body) })
          return
        }
        res = await api.post(`/api/pages/${this.pageId}/ops`, body)
        let conflict = false
        for (const r of res.results as { id: string; version: number; conflict?: boolean; deleted?: boolean }[]) {
          const op = ops.find((o) => o.id === r.id)
          if (!op) continue
          if (r.conflict) conflict = true
          if (op.op === 'del' || r.deleted) this.known.delete(r.id)
          else this.known.set(r.id, { version: r.version, position: op.position, json: op._json })
        }
        if (conflict) this.hooks.onConflict?.()
        this.hooks.onState?.(this.pending ? 'saving' : 'saved')
      } catch (e: any) {
        this.pending = true
        this.hooks.onState?.(e?.code === 'network' ? 'offline' : 'error')
        this.hooks.onError?.(e)
        if (!this.destroyed) this.timer = window.setTimeout(() => this.flush(), e?.code === 'network' ? 5000 : 3000)
      } finally {
        this.inflight = null
      }
    })()
    await this.inflight
    if (this.pending && !this.timer) this.schedule()
  }

  /** 应用服务器推来的块变化。 */
  applyRemote(ops: RemoteOp[]) {
    const { state } = this.editor
    let tr = state.tr
    const schema = state.schema
    const currentJSON = new Map<string, string>()
    const findPos = (id: string): { pos: number; node: PMNode } | null => {
      let found: { pos: number; node: PMNode } | null = null
      tr.doc.forEach((n, offset) => { if (!found && n.attrs.id === id) found = { pos: offset, node: n } })
      return found
    }
    this.topNodes().forEach((n) => currentJSON.set(n.attrs.id, canonical(n.toJSON())))
    const isDirty = (id: string) => {
      const k = this.known.get(id)
      const cur = currentJSON.get(id)
      if (!k) return cur !== undefined // 本地新建还没提交的块
      return cur !== undefined && cur !== k.json
    }
    let changed = false
    for (const op of ops) {
      if (isDirty(op.id)) continue // 本地有未提交的修改：保留本地，下次提交时由服务器记录冲突
      const at = findPos(op.id)
      if (op.op === 'del') {
        if (at) { tr = tr.delete(at.pos, at.pos + at.node.nodeSize); changed = true }
        this.known.delete(op.id)
        continue
      }
      let node: PMNode
      try { node = schema.nodeFromJSON(op.data) } catch { continue }
      const position = op.position || this.known.get(op.id)?.position || ''
      if (at) tr = tr.delete(at.pos, at.pos + at.node.nodeSize)
      // 按位置找插入点
      let insertAt = tr.doc.content.size
      let placed = false
      tr.doc.forEach((n, offset) => {
        if (placed) return
        const k = this.known.get(n.attrs.id)
        if (k && n.attrs.id !== op.id && k.position > position) { insertAt = offset; placed = true }
      })
      tr = tr.insert(insertAt, node)
      this.known.set(op.id, { version: op.version || 1, position, json: canonical(op.data) })
      changed = true
    }
    if (changed) {
      tr.setMeta(REMOTE_META, true).setMeta('addToHistory', false)
      this.editor.view.dispatch(tr)
    }
  }

  destroy() {
    this.destroyed = true
    clearTimeout(this.timer)
  }
}
