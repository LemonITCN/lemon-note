import { describe, it, expect } from 'vitest'
import { between, lis } from './fracidx'

describe('fracidx 与服务端一致', () => {
  const cases: [string | null, string | null, string][] = [
    [null, null, 'a0'], [null, 'a0', 'Zz'], [null, 'Zz', 'Zy'], ['a0', null, 'a1'], ['a1', null, 'a2'], ['a0', 'a1', 'a0V'],
    ['a1', 'a2', 'a1V'], ['a0V', 'a1', 'a0l'], ['Zz', 'a0', 'ZzV'], ['Zz', 'a1', 'a0'], [null, 'Y00', 'Xzzz'], ['bzz', null, 'c000'],
    ['a0', 'a0V', 'a0G'], ['a0', 'a0G', 'a08'], ['b125', 'b129', 'b127'], ['a0', 'a1V', 'a1'], ['Zz', 'a01', 'a0'], [null, 'a0V', 'a0'], [null, 'b999', 'b99'],
  ]
  for (const [a, b, want] of cases) it(`${a} ${b}`, () => expect(between(a, b)).toBe(want))
  it('lis', () => expect(lis(['a', 'c', 'b', 'd'])).toEqual([0, 2, 3]))
})
