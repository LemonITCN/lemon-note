import { api } from '@/api/client'

export interface UploadedFile { id: string; name: string; size: number; mime: string; width?: number; height?: number }

/** 上传附件，返回文件信息。 */
export async function uploadFile(file: File, pageId?: string, spaceId?: string): Promise<UploadedFile> {
  const fd = new FormData()
  fd.append('file', file, file.name)
  if (pageId) fd.append('page_id', pageId)
  if (spaceId) fd.append('space_id', spaceId)
  const r = await api.post<{ file: UploadedFile }>('/api/files', fd)
  return r.file
}

/** lemon://file/<id> → 可访问的地址。 */
export function fileURL(src: string | undefined | null): string {
  if (!src) return ''
  if (src.startsWith('lemon://file/')) return '/api/files/' + src.slice('lemon://file/'.length)
  return src
}

export function fileSrc(id: string) { return 'lemon://file/' + id }

export function formatSize(n: number) {
  if (!n) return ''
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

/** 选择本地文件。 */
export function pickFile(accept = ''): Promise<File | null> {
  return new Promise((resolve) => {
    const input = document.createElement('input')
    input.type = 'file'
    input.accept = accept
    input.onchange = () => resolve(input.files?.[0] || null)
    input.click()
  })
}

/** 拖入 / 粘贴的文件先放这里，节点视图按令牌取出后上传。 */
export const pendingUploads = new Map<string, File>()
export function stashUpload(f: File): string {
  const token = 'up_' + Math.random().toString(36).slice(2) + Date.now().toString(36)
  pendingUploads.set(token, f)
  return token
}
export function takeUpload(token: string | null | undefined): File | undefined {
  if (!token) return undefined
  const f = pendingUploads.get(token)
  pendingUploads.delete(token)
  return f
}
