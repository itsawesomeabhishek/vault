import { useState } from 'react'
import type { AppState, MemberStatus, NodeStatus } from '../../../shared/types'
import { formatBytes } from '../../../shared/policy'
import { Card, ProgressBar, Stat, StatusBadge, useNotify, type Health } from '../components'
import { api } from '../lib'

const MEMBER_HEALTH: Record<MemberStatus['state'], [Health, string]> = {
  alive: ['healthy', 'Online'],
  suspect: ['degraded', 'Not responding'],
  dead: ['down', 'Offline'],
  left: ['down', 'Left'],
  unknown: ['unknown', 'Unknown']
}

export function Cluster({ app, status }: { app: AppState; status: NodeStatus | null }): JSX.Element {
  const notify = useNotify()
  const [invite, setInvite] = useState<{ code: string; expires: string } | null>(null)
  const localIds = new Set(app.nodes.map((n) => n.id).filter(Boolean))

  const createInvite = async (): Promise<void> => {
    const res = await api<{ code: string; expires: string }>('POST', '/v1/invites')
    if (res.ok && res.data) setInvite(res.data)
    else notify(`Could not create invite: ${res.error}`, 'error')
  }

  const copy = async (): Promise<void> => {
    if (!invite) return
    await window.vault.copy(invite.code)
    notify('Invite code copied to the clipboard', 'success')
  }

  const zones = new Set(status?.members.map((m) => m.zone))
  const alive = status?.members.filter((m) => m.state === 'alive').length ?? 0

  return (
    <>
      <h1>Cluster</h1>
      <Card title="Overview">
        <dl className="stats">
          <Stat label="Nodes online" value={`${alive} / ${status?.members.length ?? 0}`} />
          <Stat label="Computers (zones)" value={zones.size} />
          <Stat label="Objects on this node" value={status?.objects ?? 0} />
          <Stat label="Repair queue" value={status?.repairQueue ?? 0} hint="objects being repaired" />
          <Stat label="Held for offline nodes" value={status?.hints ?? 0} hint="hinted handoff" />
          <Stat label="Encryption at rest" value={status?.encryptAtRest ? 'AES-256-GCM' : 'Off'} />
        </dl>
      </Card>

      <Card title="All nodes">
        <div className="table-wrap">
          <table>
            <caption className="sr-only">Cluster members</caption>
            <thead>
              <tr>
                <th scope="col">Node</th>
                <th scope="col">Computer (zone)</th>
                <th scope="col">Address</th>
                <th scope="col">Share of data</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {(status?.members ?? []).map((m) => {
                const [h, label] = MEMBER_HEALTH[m.state] ?? MEMBER_HEALTH.unknown
                return (
                  <tr key={m.id}>
                    <th scope="row" className="mono">
                      {m.id}
                      {localIds.has(m.id) && <span className="tag">this computer</span>}
                    </th>
                    <td>{m.zone}</td>
                    <td className="mono">{m.rpcAddr}</td>
                    <td>{(m.share * 100).toFixed(0)}%</td>
                    <td>
                      <StatusBadge health={h} label={label} />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title="Nodes on this computer">
        <div className="table-wrap">
          <table>
            <caption className="sr-only">Local storage nodes</caption>
            <thead>
              <tr>
                <th scope="col">Local node</th>
                <th scope="col">Ports (API / gRPC / gossip)</th>
                <th scope="col">Process</th>
                <th scope="col">
                  <span className="sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {app.nodes.map((n) => (
                <tr key={n.index}>
                  <th scope="row">
                    Node {n.index + 1} <span className="mono muted">{n.id ?? ''}</span>
                  </th>
                  <td className="mono">
                    {n.apiPort} / {n.rpcPort} / {n.gossipPort}
                  </td>
                  <td>
                    <StatusBadge
                      health={n.state === 'running' ? 'healthy' : n.state === 'starting' ? 'working' : n.state === 'crashed' ? 'down' : 'unknown'}
                      label={n.state === 'crashed' ? `Crashed, restarting (${n.lastError ?? ''})` : n.state[0]!.toUpperCase() + n.state.slice(1)}
                    />
                  </td>
                  <td>
                    {n.state === 'stopped' ? (
                      <button type="button" className="btn btn-small" onClick={() => void window.vault.startNode(n.index)}>
                        Start node {n.index + 1}
                      </button>
                    ) : (
                      <button type="button" className="btn btn-small btn-warning" onClick={() => void window.vault.stopNode(n.index)}>
                        Stop node {n.index + 1}
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {status && status.maxBytes > 0 && (
          <div className="field">
            <p id="usage-label">
              Disk used by this node: {formatBytes(status.usedBytes)} of {formatBytes(status.maxBytes)}
            </p>
            <ProgressBar value={status.usedBytes} max={status.maxBytes} label="Disk used by this node" />
          </div>
        )}
      </Card>

      <Card title="Add another computer">
        <ol className="steps">
          <li>Install Vault on the other computer.</li>
          <li>Create an invite here and send the code to that computer (valid for 30 minutes, single use).</li>
          <li>On the other computer choose “Join an existing cluster” and paste the code.</li>
        </ol>
        <div className="row">
          <button type="button" className="btn btn-primary" onClick={() => void createInvite()}>
            Create invite
          </button>
          {invite && (
            <button type="button" className="btn" onClick={() => void copy()}>
              Copy code
            </button>
          )}
        </div>
        {invite && (
          <div className="field">
            <label htmlFor="invite-code">Invite code (expires {new Date(invite.expires).toLocaleTimeString()})</label>
            <textarea id="invite-code" readOnly rows={3} value={invite.code} className="mono" onFocus={(e) => e.currentTarget.select()} />
          </div>
        )}
      </Card>
    </>
  )
}
