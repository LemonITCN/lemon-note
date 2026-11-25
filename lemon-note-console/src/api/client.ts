// 统一的接口调用：自动带 CSRF 令牌与实时连接 id，错误统一成 ApiError。
import { publicPath } from '@/lib/publicCtx'

let csrf = ''
let connId = ''

export function setCsrf(t: string | undefined) { csrf = t || '' }
export function getCsrf() { return csrf }
export function setConnId(id: string) { connId = id }
export function getConnId() { return connId }

export class ApiError extends Error {
  code: string
  status: number
  extra?: any
  constructor(status: number, code: string, message: string, extra?: any) {
    super(message)
    this.status = status
    this.code = code
    this.extra = extra
  }
}

type Listener = (e: ApiError) => void
const authListeners: Listener[] = []
export function onAuthError(fn: Listener) { authListeners.push(fn) }

export interface RequestOptions {
  signal?: AbortSignal
  raw?: boolean
  headers?: Record<string, string>
}

export async function request<T = any>(method: string, path: string, body?: unknown, opts: RequestOptions = {}): Promise<T> {
  path = publicPath(path)
  const headers: Record<string, string> = { Accept: 'application/json', ...(opts.headers || {}) }
  let payload: BodyInit | undefined
  if (body instanceof FormData) {
    payload = body
  } else if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
    payload = JSON.stringify(body)
  }
  if (method !== 'GET' && csrf) headers['X-CSRF-Token'] = csrf
  if (connId) headers['X-Lemon-Conn'] = connId
  let res: Response
  try {
    res = await fetch(path, { method, headers, body: payload, credentials: 'same-origin', signal: opts.signal })
  } catch (e: any) {
    if (e?.name === 'AbortError') throw e
    throw new ApiError(0, 'network', '无法连接服务器，请检查网络')
  }
  if (opts.raw) return res as any
  let data: any = null
  const text = await res.text()
  if (text) {
    try { data = JSON.parse(text) } catch { data = null }
  }
  if (!res.ok) {
    const err = data?.error || {}
    const e = new ApiError(res.status, err.code || 'http_' + res.status, err.message || `请求失败（${res.status}）`, err.extra)
    if (res.status === 401 && e.code !== 'bad_credentials' && e.code !== 'mfa_required' && !path.startsWith('/api/public/')) authListeners.forEach((fn) => fn(e))
    throw e
  }
  return data as T
}

export const api = {
  get: <T = any>(p: string, o?: RequestOptions) => request<T>('GET', p, undefined, o),
  post: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('POST', p, b ?? {}, o),
  put: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('PUT', p, b ?? {}, o),
  patch: <T = any>(p: string, b?: unknown, o?: RequestOptions) => request<T>('PATCH', p, b ?? {}, o),
  del: <T = any>(p: string, o?: RequestOptions) => request<T>('DELETE', p, undefined, o),
}
