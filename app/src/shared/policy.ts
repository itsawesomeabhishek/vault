import type { Policy } from './types'

export const MAX_WIDTH = 16

/** Mirrors engine/internal/policy.Validate so the UI can explain problems before submitting. */
export function validatePolicy(p: Policy): string[] {
  const errors: string[] = []
  const int = (v: number): boolean => Number.isInteger(v)
  if (![p.n, p.k, p.w, p.r].every(int)) errors.push('All values must be whole numbers.')
  if (p.n < 1 || p.n > MAX_WIDTH) errors.push(`Total copies/shards (N) must be between 1 and ${MAX_WIDTH}.`)
  if (p.k < 1 || p.k > p.n) errors.push('Data shards (K) must be between 1 and N.')
  if (p.k > 1 && p.n === p.k) errors.push('Erasure coding needs at least one parity shard (N > K).')
  if (p.w < p.k || p.w > p.n) errors.push('Write quorum (W) must be between K and N.')
  if (p.r < 1 || p.r > p.n) errors.push('Read quorum (R) must be between 1 and N.')
  if (p.r + p.w <= p.n) errors.push('R + W must be greater than N so every read sees the latest write.')
  return errors
}

export function overhead(p: Policy): number {
  return p.n / p.k
}

export function faultTolerance(p: Policy): number {
  return p.n - p.k
}

export function ackFaultTolerance(p: Policy): number {
  return p.w - p.k
}

export function describePolicy(p: Policy): string {
  return p.k > 1
    ? `Erasure coding ${p.k}+${p.n - p.k}: stores ${overhead(p).toFixed(2)}x, survives ${faultTolerance(p)} lost node(s)`
    : `Replication x${p.n}: stores ${overhead(p).toFixed(2)}x, survives ${faultTolerance(p)} lost node(s)`
}

export function formatBytes(n: number): string {
  if (!Number.isFinite(n) || n < 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let i = 0
  let v = n
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024
    i++
  }
  return `${v.toFixed(i === 0 ? 0 : 1)} ${units[i]}`
}
