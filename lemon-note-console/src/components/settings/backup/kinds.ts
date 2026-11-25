// 备份目标类型与各自的配置字段（与服务端 internal/backup/target 的配置结构一致）。
export interface Field { key: string; type: 'text' | 'password' | 'number' | 'switch' | 'textarea'; ph?: string; def?: any; optional?: boolean; tip?: boolean }

export const KINDS: Record<string, Field[]> = {
  local: [{ key: 'path', type: 'text', ph: '/mnt/nas/lemon-note-backup', tip: true }],
  s3: [
    { key: 'endpoint', type: 'text', ph: 's3.amazonaws.com / minio.example.com:9000' },
    { key: 'region', type: 'text', ph: 'us-east-1', optional: true },
    { key: 'bucket', type: 'text' },
    { key: 'prefix', type: 'text', optional: true, ph: 'lemon-note' },
    { key: 'access_key', type: 'text' },
    { key: 'secret_key', type: 'password' },
    { key: 'use_ssl', type: 'switch', def: true },
    { key: 'path_style', type: 'switch', def: false, tip: true },
  ],
  cos: [
    { key: 'region', type: 'text', ph: 'ap-guangzhou' },
    { key: 'bucket', type: 'text', ph: 'backup-1250000000' },
    { key: 'prefix', type: 'text', optional: true, ph: 'lemon-note' },
    { key: 'secret_id', type: 'text' },
    { key: 'secret_key', type: 'password' },
  ],
  oss: [
    { key: 'region', type: 'text', ph: 'cn-hangzhou' },
    { key: 'bucket', type: 'text' },
    { key: 'prefix', type: 'text', optional: true, ph: 'lemon-note' },
    { key: 'access_key_id', type: 'text' },
    { key: 'access_key_secret', type: 'password' },
    { key: 'endpoint', type: 'text', optional: true, tip: true },
    { key: 'internal', type: 'switch', def: false, tip: true },
  ],
  webdav: [
    { key: 'url', type: 'text', ph: 'https://dav.jianguoyun.com/dav/' },
    { key: 'username', type: 'text' },
    { key: 'password', type: 'password' },
    { key: 'prefix', type: 'text', optional: true, ph: 'lemon-note' },
  ],
  sftp: [
    { key: 'host', type: 'text' },
    { key: 'port', type: 'number', def: 22 },
    { key: 'username', type: 'text' },
    { key: 'password', type: 'password', optional: true },
    { key: 'private_key', type: 'textarea', optional: true, tip: true },
    { key: 'passphrase', type: 'password', optional: true },
    { key: 'path', type: 'text', optional: true, ph: 'backup/lemon-note' },
    { key: 'host_key_sha256', type: 'text', optional: true, tip: true },
  ],
  baidu: [
    { key: 'app_key', type: 'text', tip: true },
    { key: 'secret_key', type: 'password' },
    { key: 'app_name', type: 'text', tip: true },
    { key: 'prefix', type: 'text', optional: true, ph: 'lemon-note' },
  ],
}

export const KIND_ORDER = ['local', 's3', 'cos', 'oss', 'webdav', 'sftp', 'baidu']
export const MASK = '********'

export function defaults(kind: string) {
  const c: Record<string, any> = {}
  for (const f of KINDS[kind] || []) c[f.key] = f.def ?? (f.type === 'switch' ? false : f.type === 'number' ? 0 : '')
  return c
}
