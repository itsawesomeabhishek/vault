import { useEffect, useRef, useState } from 'react'
import type { Inspection, SlotStatus } from '../../../shared/types'
import { formatBytes } from '../../../shared/policy'
import { Card, StatusBadge, useNotify, type Health } from '../components'
import { api, inspectPath } from '../lib'

function slotHealth(s: SlotStatus): [Health, string] {
  if (!s.node) return ['unknown', 'Unassigned']
  if (!s.alive) return ['down', 'Node offline']
  if (s.healthy) return ['healthy', 'Healthy']
  return ['degraded', s.error ? `Repairing: ${s.error}` : 'Repairing']
}

export function ObjectDetail(props: { bucket: string; objectKey: string; demoMode: boolean; onBack: () => void }): JSX.Element {
  const notify = useNotify()
  const [ins, setIns] = useState<Inspection | null>(null)
  const [error, setError] = useState<string | null>(null)
  const heading = useRef<HTMLHeadingElement>(null)

  const load = async (): Promise<void> => {
    const res = await api<Inspection>('GET', inspectPath(props.bucket, props.objectKey))
    if (res.ok) {
      setIns(res.data)
      setError(null)
    } else setError(res.error)
  }

  useEffect(() => {
    heading.current?.focus()
    void load()
    const t = setInterval(() => void load(), 2000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.bucket, props.objectKey])

  const chaos = async (kind: 'corrupt' | 'drop', node: number): Promise<void> => {
    const res = await api('POST', `/v1/chaos/${kind}`, { bucket: props.bucket, key: props.objectKey }, node)
    notify(
      res.ok ? (kind === 'corrupt' ? 'Flipped bits in a local copy. Watch Vault detect and repair it.' : 'Deleted a local copy. Vault will rebuild it.') : `Failed: ${res.error}`,
      res.ok ? 'info' : 'error'
    )
    if (res.ok && kind === 'corrupt') await api('POST', '/v1/maintenance/scrub', undefined, node)
    void load()
  }

  const repair = async (): Promise<void> => {
    const res = await api('POST', `/v1/buckets/${props.bucket}/repair/${props.objectKey.split('/').map(encodeURIComponent).join('/')}`)
    notify(res.ok ? 'Repair scheduled' : `Failed: ${res.error}`, res.ok ? 'success' : 'error')
  }

  const overall: [Health, string] = !ins
    ? ['working', 'Checking…']
    : ins.healthySlots === ins.width
      ? ['healthy', `Fully protected (${ins.healthySlots}/${ins.width})`]
      : ins.readable
        ? ['degraded', `Under-replicated (${ins.healthySlots}/${ins.width}), repairing`]
        : ['down', 'Unreadable until nodes return']

  return (
    <>
      <button type="button" className="btn btn-link" onClick={props.onBack}>
        ← Back to scans
      </button>
      <h1 ref={heading} tabIndex={-1} className="mono">
        {props.objectKey}
      </h1>
      {error && (
        <p role="alert" className="error-text">
          {error}
        </p>
      )}
      <Card title="Protection" actions={<StatusBadge health={overall[0]} label={overall[1]} />}>
        {ins && (
          <dl className="stats">
            <div className="stat">
              <dt>Size</dt>
              <dd>{formatBytes(ins.object.size)}</dd>
            </div>
            <div className="stat">
              <dt>Policy</dt>
              <dd>{ins.object.policy}</dd>
            </div>
            <div className="stat">
              <dt>Storage overhead</dt>
              <dd>{ins.overhead.toFixed(2)}x</dd>
            </div>
            <div className="stat">
              <dt>Survives losing</dt>
              <dd>{ins.faultTolerance} node(s)</dd>
            </div>
            <div className="stat">
              <dt>SHA-256</dt>
              <dd className="mono small">{ins.object.etag.slice(0, 16)}…</dd>
            </div>
          </dl>
        )}
      </Card>
      <Card title="Where the copies live" actions={<button type="button" className="btn" onClick={() => void repair()}>Repair now</button>}>
        <p className="muted">Every copy is re-hashed on each refresh, so corruption shows up here within seconds.</p>
        <div className="table-wrap">
          <table>
            <caption className="sr-only">Replica and shard placement</caption>
            <thead>
              <tr>
                <th scope="col">{ins && ins.dataShards > 1 ? 'Shard' : 'Copy'}</th>
                <th scope="col">Node</th>
                <th scope="col">Computer (zone)</th>
                <th scope="col">Chunks verified</th>
                <th scope="col">Status</th>
              </tr>
            </thead>
            <tbody>
              {ins?.slots.map((s) => {
                const [h, label] = slotHealth(s)
                return (
                  <tr key={s.slot}>
                    <th scope="row">
                      {ins.dataShards > 1 ? (s.slot < ins.dataShards ? `Data ${s.slot + 1}` : `Parity ${s.slot - ins.dataShards + 1}`) : `#${s.slot + 1}`}
                    </th>
                    <td className="mono">{s.node || '—'}</td>
                    <td>{s.zone || '—'}</td>
                    <td>
                      {s.shardsPresent}/{s.shardsTotal}
                    </td>
                    <td>
                      <StatusBadge health={h} label={label} />
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        </div>
        {ins?.extraHolders && ins.extraHolders.length > 0 && <p className="muted">Temporary copies (hinted handoff) on: {ins.extraHolders.join(', ')}</p>}
      </Card>
      {props.demoMode && (
        <Card title="Simulate failures on this computer">
          <p className="muted">These act on the copy held by one of this computer&apos;s nodes, if it holds one.</p>
          <div className="row wrap">
            {[0, 1].map((n) => (
              <span key={n} className="row">
                <button type="button" className="btn btn-warning" onClick={() => void chaos('corrupt', n)}>
                  Corrupt copy on local node {n + 1}
                </button>
                <button type="button" className="btn btn-warning" onClick={() => void chaos('drop', n)}>
                  Delete copy on local node {n + 1}
                </button>
              </span>
            ))}
          </div>
        </Card>
      )}
    </>
  )
}
