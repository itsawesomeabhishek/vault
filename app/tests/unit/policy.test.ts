import { describe, expect, it } from 'vitest'
import { describePolicy, faultTolerance, formatBytes, overhead, validatePolicy } from '../../src/shared/policy'

describe('validatePolicy mirrors the engine rules', () => {
  it('accepts the default policies', () => {
    expect(validatePolicy({ n: 3, k: 1, w: 2, r: 2, sloppy: true })).toEqual([])
    expect(validatePolicy({ n: 3, k: 2, w: 2, r: 2, sloppy: true })).toEqual([])
  })
  it('rejects quorums that do not overlap', () => {
    expect(validatePolicy({ n: 3, k: 1, w: 1, r: 1, sloppy: false }).join()).toMatch(/R \+ W/)
  })
  it('rejects erasure coding without parity and W below K', () => {
    expect(validatePolicy({ n: 2, k: 2, w: 2, r: 1, sloppy: false }).join()).toMatch(/parity/)
    expect(validatePolicy({ n: 4, k: 3, w: 2, r: 3, sloppy: false }).join()).toMatch(/between K and N/)
  })
  it('computes overhead and fault tolerance', () => {
    expect(overhead({ n: 3, k: 2, w: 2, r: 2, sloppy: true })).toBe(1.5)
    expect(faultTolerance({ n: 3, k: 1, w: 2, r: 2, sloppy: true })).toBe(2)
    expect(describePolicy({ n: 6, k: 4, w: 5, r: 2, sloppy: false })).toContain('Erasure coding 4+2')
  })
  it('formats bytes', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(1536)).toBe('1.5 KB')
    expect(formatBytes(5 * 1024 ** 3)).toBe('5.0 GB')
  })
})
