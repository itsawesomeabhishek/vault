import { useId, useState } from 'react'
import type { AppState, NodeStatus } from '../../../shared/types'
import { Card, useNotify } from '../components'
import { api } from '../lib'

export function Chaos({ app, status }: { app: AppState; status: NodeStatus | null }): JSX.Element {
  const notify = useNotify()
  const localIds = new Set(app.nodes.map((n) => n.id))
  const remote = (status?.members ?? []).filter((m) => !localIds.has(m.id))
  const [peer, setPeer] = useState('')
  const peerId = useId()
  const partitioned = new Set(status?.partitionedFrom ?? [])

  const run = async (label: string, fn: () => Promise<{ ok: boolean; error: string | null }>): Promise<void> => {
    const res = await fn()
    notify(res.ok ? label : `${label} failed: ${res.error}`, res.ok ? 'success' : 'error')
  }

  const partition = async (enabled: boolean): Promise<void> => {
    const target = peer || remote[0]?.id
    if (!target) return notify('No remote node to partition from.', 'error')
    for (const n of app.nodes) {
      if (n.state === 'running') await api('POST', '/v1/chaos/partition', { peer: target, enabled }, n.index)
    }
    notify(enabled ? `Network link to ${target} cut from this computer` : `Link to ${target} restored`, 'success')
  }

  return (
    <>
      <h1>Failure lab</h1>
      <p className="lead">Break things on purpose and watch Vault keep your data available and repair it.</p>
      <Card title="Crash a node">
        <p>Stopping a node is the same as a power cut for that node. Reads and writes keep working, and data written meanwhile is handed back when it returns.</p>
        <div className="row wrap">
          {app.nodes.map((n) => (
            <button
              key={n.index}
              type="button"
              className={n.state === 'stopped' ? 'btn' : 'btn btn-warning'}
              onClick={() => void (n.state === 'stopped' ? window.vault.startNode(n.index) : window.vault.stopNode(n.index))}
            >
              {n.state === 'stopped' ? `Restart local node ${n.index + 1}` : `Crash local node ${n.index + 1}`}
            </button>
          ))}
        </div>
      </Card>
      <Card title="Network partition">
        <p>Cuts the link between this computer&apos;s nodes and a node on another computer, while both keep running.</p>
        <div className="row wrap">
          <div className="field inline">
            <label htmlFor={peerId}>Remote node</label>
            <select id={peerId} value={peer} onChange={(e) => setPeer(e.target.value)}>
              {remote.length === 0 && <option value="">No remote nodes yet</option>}
              {remote.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.id} ({m.zone}){partitioned.has(m.id) ? ' — cut' : ''}
                </option>
              ))}
            </select>
          </div>
          <button type="button" className="btn btn-warning" onClick={() => void partition(true)} disabled={remote.length === 0}>
            Cut link
          </button>
          <button type="button" className="btn" onClick={() => void partition(false)} disabled={remote.length === 0}>
            Restore link
          </button>
        </div>
        {partitioned.size > 0 && <p role="status">Currently cut off from: {[...partitioned].join(', ')}</p>}
      </Card>
      <Card title="Background maintenance">
        <p>These run automatically; trigger them now to see results immediately.</p>
        <div className="row wrap">
          <button type="button" className="btn" onClick={() => void run('Integrity scrub finished', () => api('POST', '/v1/maintenance/scrub'))}>
            Verify all data now (scrub)
          </button>
          <button type="button" className="btn" onClick={() => void run('Anti-entropy round finished', () => api('POST', '/v1/maintenance/anti-entropy'))}>
            Compare replicas now (anti-entropy)
          </button>
          <button
            type="button"
            className="btn"
            onClick={() => void run('All injected faults removed', async () => {
              const results = await Promise.all(app.nodes.filter((n) => n.state === 'running').map((n) => api('POST', '/v1/chaos/heal', undefined, n.index)))
              return results.find((r) => !r.ok) ?? { ok: true, error: null }
            })}
          >
            Heal everything
          </button>
        </div>
      </Card>
    </>
  )
}
