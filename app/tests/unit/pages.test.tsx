import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import { NoticeProvider } from '../../src/renderer/src/components'
import { App } from '../../src/renderer/src/App'
import { Activity } from '../../src/renderer/src/pages/Activity'
import { ObjectDetail } from '../../src/renderer/src/pages/ObjectDetail'
import { installBridge } from './setup'
import type { ApiRequest, AppState, Inspection, NodeStatus, VaultEvent } from '../../src/shared/types'

const ready: AppState = {
  setupComplete: true,
  zone: 'laptop-a',
  storageDir: 'C:\\data',
  demoMode: true,
  advertise: '',
  nodes: [
    { index: 0, id: 'n1', state: 'running', apiPort: 18080, rpcPort: 19000, gossipPort: 17946, lastError: null, restarts: 0 }
  ],
  version: '1.0.0'
}

const status: NodeStatus = {
  id: 'n1',
  zone: 'laptop-a',
  usedBytes: 0,
  maxBytes: 0,
  objects: 1,
  tombstones: 0,
  logicalBytes: 0,
  hints: 0,
  repairQueue: 0,
  encryptAtRest: true,
  partitionedFrom: [],
  generatedAt: new Date().toISOString(),
  members: [{ id: 'n1', zone: 'laptop-a', rpcAddr: '127.0.0.1:19000', state: 'alive', share: 1, self: true, since: '' }],
  counters: { put_ok: 2, get_ok: 1, corruption_detected: 0, repairs_completed: 1 },
  latencies: { put_latency: { count: 2, meanMs: 12, p50Ms: 10, p99Ms: 20 }, time_to_repair: { count: 1, meanMs: 40, p50Ms: 40, p99Ms: 40 } }
}

describe('shell accessibility', () => {
  it('exposes a skip link, current page, and text-not-colour status', async () => {
    installBridge({
      state: async () => ready,
      api: async <T,>(req: ApiRequest) => {
        const data = req.path === '/v1/status' ? status : req.path === '/v1/buckets' ? [] : { items: [] }
        return { ok: true, status: 200, data: data as T, error: null }
      }
    })
    render(
      <NoticeProvider>
        <App />
      </NoticeProvider>
    )
    expect(await screen.findByRole('link', { name: 'Skip to main content' })).toBeInTheDocument()
    expect(screen.getByRole('navigation', { name: 'Main' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Scans' })).toHaveAttribute('aria-current', 'page')
    await userEvent.click(screen.getByRole('button', { name: 'Failure lab' }))
    expect(screen.getByRole('heading', { name: 'Failure lab' })).toBeVisible()
    expect(screen.getByRole('button', { name: 'Failure lab' })).toHaveAttribute('aria-current', 'page')
  })
})

describe('activity live region', () => {
  it('announces events in a log and never uses colour alone', () => {
    const events: VaultEvent[] = [
      { id: 1, time: new Date().toISOString(), type: 'object.put', severity: 'info', node: 'n1', message: 'stored scan' },
      { id: 2, time: new Date().toISOString(), type: 'shard.corrupt', severity: 'error', node: 'n1', message: 'checksum mismatch' }
    ]
    render(<Activity events={events} status={status} />)
    expect(screen.getByRole('log', { name: 'Cluster events, newest first' })).toBeInTheDocument()
    expect(screen.getByText('Error')).toBeVisible()
    expect(screen.getByText('checksum mismatch')).toBeVisible()
    expect(screen.getByText(/median time to repair/)).toBeInTheDocument()
  })
})

describe('object detail replica table', () => {
  it('labels every slot with text status', async () => {
    const ins: Inspection = {
      object: {
        bucket: 'scans',
        key: 'p/1.dcm',
        size: 12,
        contentType: 'application/dicom',
        etag: 'abc',
        modified: new Date().toISOString(),
        policy: 'replicated x3'
      },
      width: 3,
      dataShards: 1,
      healthySlots: 2,
      readable: true,
      overhead: 3,
      faultTolerance: 2,
      extraHolders: [],
      slots: [
        { slot: 0, node: 'n1', zone: 'a', alive: true, hasManifest: true, versionMatch: true, shardsPresent: 1, shardsTotal: 1, healthy: true },
        { slot: 1, node: 'n2', zone: 'b', alive: true, hasManifest: true, versionMatch: true, shardsPresent: 1, shardsTotal: 1, healthy: false, error: 'checksum mismatch' },
        { slot: 2, node: 'n3', zone: 'a', alive: false, hasManifest: false, versionMatch: false, shardsPresent: 0, shardsTotal: 1, healthy: false }
      ]
    }
    installBridge({
      api: async <T,>() => ({ ok: true, status: 200, data: ins as T, error: null })
    })
    render(
      <NoticeProvider>
        <ObjectDetail bucket="scans" objectKey="p/1.dcm" demoMode onBack={() => undefined} />
      </NoticeProvider>
    )
    expect(await screen.findByText('Healthy')).toBeVisible()
    expect(screen.getByText(/Repairing: checksum mismatch/)).toBeVisible()
    expect(screen.getByText('Node offline')).toBeVisible()
    expect(screen.getByText(/Under-replicated/)).toBeVisible()
  })
})
