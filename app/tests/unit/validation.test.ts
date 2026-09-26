import { describe, expect, it } from 'vitest'
import { apiRequestSchema, setupSchema } from '../../src/main/validation'

describe('IPC path allow-list', () => {
  const ok = [
    '/v1/status',
    '/v1/metrics',
    '/v1/buckets',
    '/v1/buckets/scans',
    '/v1/buckets/scans/objects',
    '/v1/buckets/scans/objects?patient=A-12',
    '/v1/buckets/scans/objects/patients%2F1%2Fct.dcm',
    '/v1/buckets/scans/inspect/ct.dcm',
    '/v1/buckets/scans/repair/ct.dcm',
    '/v1/maintenance/scrub',
    '/v1/maintenance/anti-entropy',
    '/v1/audit?limit=50',
    '/v1/invites',
    '/v1/chaos/corrupt'
  ]
  it.each(ok)('allows %s', (path) => {
    expect(apiRequestSchema.safeParse({ method: 'GET', path }).success).toBe(true)
  })
  it('rejects traversal and unknown endpoints', () => {
    expect(apiRequestSchema.safeParse({ method: 'GET', path: '/v1/../etc/passwd' }).success).toBe(false)
    expect(apiRequestSchema.safeParse({ method: 'GET', path: '/v1/events' }).success).toBe(false)
    expect(apiRequestSchema.safeParse({ method: 'DELETE', path: '/v1/chaos/heal/extra' }).success).toBe(false)
  })
})

describe('setup payload', () => {
  it('requires an invite that starts with vault1. when joining', () => {
    const bad = setupSchema.safeParse({ mode: 'join', zone: 'lab-b', maxGiB: 10, demoMode: true, inviteCode: 'nope' })
    expect(bad.success).toBe(false)
    const good = setupSchema.safeParse({ mode: 'join', zone: 'lab-b', maxGiB: 10, demoMode: true, inviteCode: 'vault1.abc' })
    expect(good.success).toBe(true)
  })
})
