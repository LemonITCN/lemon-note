// 分数索引，与服务端 internal/fracidx 同一算法（rocicorp/fractional-indexing），两边共用测试向量。
const DIGITS = '0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz'
const SMALLEST = 'A' + '0'.repeat(26)

function midpoint(a: string, b: string | null): string {
  if (b !== null && a >= b) throw new Error('fracidx: a >= b')
  if (a.slice(-1) === '0' || (b && b.slice(-1) === '0')) throw new Error('fracidx: trailing zero')
  if (b) {
    let n = 0
    while ((a[n] || '0') === b[n]) n++
    if (n > 0) return b.slice(0, n) + midpoint(a.slice(n), b.slice(n))
  }
  const digitA = a ? DIGITS.indexOf(a[0]) : 0
  const digitB = b !== null ? DIGITS.indexOf(b[0]) : DIGITS.length
  if (digitB - digitA > 1) return DIGITS[Math.round(0.5 * (digitA + digitB))]
  if (b && b.length > 1) return b.slice(0, 1)
  return DIGITS[digitA] + midpoint(a.slice(1), null)
}

function intLength(head: string): number {
  if (head >= 'a' && head <= 'z') return head.charCodeAt(0) - 97 + 2
  if (head >= 'A' && head <= 'Z') return 90 - head.charCodeAt(0) + 2
  throw new Error('fracidx: invalid head')
}

function intPart(key: string) {
  const l = intLength(key[0])
  if (l > key.length) throw new Error('fracidx: invalid key')
  return key.slice(0, l)
}

export function validate(key: string): boolean {
  try {
    if (!key || key === SMALLEST) return false
    const i = intPart(key)
    const f = key.slice(i.length)
    if (f.slice(-1) === '0') return false
    for (const ch of key) if (DIGITS.indexOf(ch) < 0) return false
    return true
  } catch { return false }
}

function increment(x: string): string | null {
  const [head, ...digs] = x.split('')
  let carry = true
  for (let i = digs.length - 1; carry && i >= 0; i--) {
    const d = DIGITS.indexOf(digs[i]) + 1
    if (d === DIGITS.length) digs[i] = '0'
    else { digs[i] = DIGITS[d]; carry = false }
  }
  if (carry) {
    if (head === 'Z') return 'a0'
    if (head === 'z') return null
    const h = String.fromCharCode(head.charCodeAt(0) + 1)
    if (h > 'a') digs.push('0')
    else digs.pop()
    return h + digs.join('')
  }
  return head + digs.join('')
}

function decrement(x: string): string | null {
  const [head, ...digs] = x.split('')
  let borrow = true
  for (let i = digs.length - 1; borrow && i >= 0; i--) {
    const d = DIGITS.indexOf(digs[i]) - 1
    if (d === -1) digs[i] = DIGITS.slice(-1)
    else { digs[i] = DIGITS[d]; borrow = false }
  }
  if (borrow) {
    if (head === 'a') return 'Z' + DIGITS.slice(-1)
    if (head === 'A') return null
    const h = String.fromCharCode(head.charCodeAt(0) - 1)
    if (h < 'Z') digs.push(DIGITS.slice(-1))
    else digs.pop()
    return h + digs.join('')
  }
  return head + digs.join('')
}

/** 返回严格位于 a 与 b 之间的键；null 表示开头 / 结尾。 */
export function between(a: string | null, b: string | null): string {
  if (a === '') a = null
  if (b === '') b = null
  if (a !== null && b !== null && a >= b) throw new Error('fracidx: a >= b')
  if (a === null) {
    if (b === null) return 'a0'
    const ib = intPart(b)
    const fb = b.slice(ib.length)
    if (ib === SMALLEST) return ib + midpoint('', fb)
    if (ib < b) return ib
    const r = decrement(ib)
    if (r === null) throw new Error('fracidx: cannot decrement')
    return r
  }
  if (b === null) {
    const ia = intPart(a)
    const fa = a.slice(ia.length)
    const i = increment(ia)
    return i === null ? ia + midpoint(fa, null) : i
  }
  const ia = intPart(a)
  const fa = a.slice(ia.length)
  const ib = intPart(b)
  const fb = b.slice(ib.length)
  if (ia === ib) return ia + midpoint(fa, fb)
  const i = increment(ia)
  if (i === null) throw new Error('fracidx: cannot increment')
  if (i < b) return i
  return ia + midpoint(fa, null)
}

/** 最长严格递增子序列的下标。 */
export function lis(a: string[]): number[] {
  const tails: number[] = []
  const prev: number[] = new Array(a.length).fill(-1)
  for (let i = 0; i < a.length; i++) {
    let lo = 0, hi = tails.length
    while (lo < hi) {
      const mid = (lo + hi) >> 1
      if (a[tails[mid]] < a[i]) lo = mid + 1
      else hi = mid
    }
    prev[i] = lo > 0 ? tails[lo - 1] : -1
    tails[lo] = i
  }
  const out: number[] = []
  let k = tails.length ? tails[tails.length - 1] : -1
  while (k >= 0) { out.push(k); k = prev[k] }
  return out.reverse()
}
