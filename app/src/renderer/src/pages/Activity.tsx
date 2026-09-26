import { useId, useState } from 'react'
import type { NodeStatus, VaultEvent } from '../../../shared/types'
import { Card, Stat, StatusBadge } from '../components'
import { formatTime } from '../lib'

const SEVERITY: Record<VaultEvent['severity'], ['healthy' | 'degraded' | 'down', string]> = {
  info: ['healthy', 'Info'],
  warning: ['degraded', 'Warning'],
  error: ['down', 'Error']
}

export function Activity({ events, status }: { events: VaultEvent[]; status: NodeStatus | null }): JSX.Element {
  const [filter, setFilter] = useState<'all' | 'problems'>('all')
  const filterId = useId()
  const shown = filter === 'all' ? events : events.filter((e) => e.severity !== 'info')
  const c = status?.counters ?? {}
  const lat = status?.latencies ?? {}
  const ms = (v?: number): string => (v === undefined ? '—' : `${v.toFixed(v < 10 ? 1 : 0)} ms`)

  return (
    <>
      <h1>Activity</h1>
      <Card title="Health metrics (this node)">
        <dl className="stats">
          <Stat label="Uploads" value={c['put_ok'] ?? 0} hint={`p50 ${ms(lat['put_latency']?.p50Ms)} · p99 ${ms(lat['put_latency']?.p99Ms)}`} />
          <Stat label="Downloads" value={c['get_ok'] ?? 0} hint={`p50 ${ms(lat['get_latency']?.p50Ms)} · p99 ${ms(lat['get_latency']?.p99Ms)}`} />
          <Stat label="Corruption detected" value={c['corruption_detected'] ?? 0} />
          <Stat label="Repairs completed" value={c['repairs_completed'] ?? 0} hint={`median time to repair ${ms(lat['time_to_repair']?.p50Ms)}`} />
          <Stat label="Shards rebuilt" value={c['shards_repaired'] ?? 0} />
          <Stat label="Hedged reads" value={c['hedged_requests'] ?? 0} hint="backup requests to slow replicas" />
          <Stat label="Anti-entropy rounds" value={c['anti_entropy_rounds'] ?? 0} />
          <Stat label="Rejected for quorum" value={(c['put_unavailable'] ?? 0) + (c['get_unavailable'] ?? 0)} />
        </dl>
      </Card>
      <Card
        title="Live events"
        actions={
          <div className="field inline">
            <label htmlFor={filterId}>Show</label>
            <select id={filterId} value={filter} onChange={(e) => setFilter(e.target.value as 'all' | 'problems')}>
              <option value="all">All events</option>
              <option value="problems">Warnings and errors</option>
            </select>
          </div>
        }
      >
        <ol className="events" role="log" aria-live="polite" aria-relevant="additions" aria-label="Cluster events, newest first">
          {shown.length === 0 && <li className="muted">No events yet.</li>}
          {shown.map((e) => (
            <li key={`${e.node}-${e.id}-${e.time}`} className="event">
              <StatusBadge health={SEVERITY[e.severity]?.[0] ?? 'unknown'} label={SEVERITY[e.severity]?.[1] ?? e.severity} />
              <div>
                <p>
                  <strong>{e.message}</strong>
                </p>
                <p className="muted small">
                  <time dateTime={e.time}>{formatTime(e.time)}</time> · {e.type} · {e.node}
                  {e.key ? ` · ${e.bucket}/${e.key}` : ''}
                </p>
              </div>
            </li>
          ))}
        </ol>
      </Card>
    </>
  )
}
