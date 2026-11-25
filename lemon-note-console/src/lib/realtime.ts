// 实时连接：自动重连，维护页面订阅，按消息类型分发。
import { setConnId } from '@/api/client'

type Handler = (data: any) => void

class Realtime {
  private ws: WebSocket | null = null
  private handlers = new Map<string, Set<Handler>>()
  private subs = new Set<string>()
  private retry = 0
  private timer: number | undefined
  private stopped = true
  connected = false

  start() {
    if (!this.stopped) return
    this.stopped = false
    this.connect()
  }

  stop() {
    this.stopped = true
    clearTimeout(this.timer)
    this.ws?.close()
    this.ws = null
  }

  private connect() {
    if (this.stopped) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/api/ws`)
    this.ws = ws
    ws.onopen = () => {
      this.retry = 0
      this.connected = true
      for (const ch of this.subs) ws.send(JSON.stringify({ type: 'sub', channel: ch }))
      this.emit('open', null)
    }
    ws.onmessage = (ev) => {
      let msg: any
      try { msg = JSON.parse(ev.data) } catch { return }
      if (msg.type === 'hello') setConnId(msg.data.conn_id)
      this.emit(msg.type, msg.data)
    }
    ws.onclose = () => {
      this.connected = false
      setConnId('')
      this.emit('close', null)
      if (this.stopped) return
      const delay = Math.min(30000, 500 * 2 ** this.retry++) + Math.random() * 500
      this.timer = window.setTimeout(() => this.connect(), delay)
    }
    ws.onerror = () => ws.close()
  }

  send(type: string, data: any) {
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ type, data }))
  }

  subscribe(channel: string) {
    this.subs.add(channel)
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ type: 'sub', channel }))
  }

  unsubscribe(channel: string) {
    this.subs.delete(channel)
    if (this.ws?.readyState === WebSocket.OPEN) this.ws.send(JSON.stringify({ type: 'unsub', channel }))
  }

  on(type: string, fn: Handler) {
    if (!this.handlers.has(type)) this.handlers.set(type, new Set())
    this.handlers.get(type)!.add(fn)
    return () => this.handlers.get(type)?.delete(fn)
  }

  private emit(type: string, data: any) {
    this.handlers.get(type)?.forEach((fn) => {
      try { fn(data) } catch (e) { console.error(e) }
    })
  }
}

export const realtime = new Realtime()
