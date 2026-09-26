import { useEffect, useRef, useState } from 'react'
import type { AppState } from '../../shared/types'
import { NoticeProvider, StatusBadge, type Health } from './components'
import { useAppState, useEvents, useStatus } from './lib'
import { Activity } from './pages/Activity'
import { Buckets } from './pages/Buckets'
import { Chaos } from './pages/Chaos'
import { Cluster } from './pages/Cluster'
import { ObjectDetail } from './pages/ObjectDetail'
import { Scans } from './pages/Scans'
import { Setup } from './pages/Setup'

type Page = 'scans' | 'buckets' | 'cluster' | 'activity' | 'chaos'

const PAGES: { id: Page; label: string; demoOnly?: boolean }[] = [
  { id: 'scans', label: 'Scans' },
  { id: 'buckets', label: 'Buckets' },
  { id: 'cluster', label: 'Cluster' },
  { id: 'activity', label: 'Activity' },
  { id: 'chaos', label: 'Failure lab', demoOnly: true }
]

export function App(): JSX.Element {
  const [app, setApp] = useAppState()
  if (!app) {
    return (
      <main id="main" className="loading" aria-busy="true">
        <p>Loading Vault…</p>
      </main>
    )
  }
  return <NoticeProvider>{app.setupComplete ? <Shell app={app} /> : <Setup initial={app} onDone={setApp} />}</NoticeProvider>
}

function Shell({ app }: { app: AppState }): JSX.Element {
  const [page, setPage] = useState<Page>('scans')
  const [detail, setDetail] = useState<{ bucket: string; key: string } | null>(null)
  const { status, error } = useStatus()
  const events = useEvents()
  const mainRef = useRef<HTMLElement>(null)

  useEffect(() => {
    document.title = `Vault — ${detail ? detail.key : (PAGES.find((p) => p.id === page)?.label ?? '')}`
  }, [page, detail])

  const go = (p: Page): void => {
    setDetail(null)
    setPage(p)
    mainRef.current?.focus()
  }

  const members = status?.members ?? []
  const alive = members.filter((m) => m.state === 'alive').length
  const health: [Health, string] = error
    ? ['down', 'Local nodes not responding']
    : !status
      ? ['working', 'Connecting…']
      : alive === members.length
        ? ['healthy', `All ${alive} nodes online`]
        : ['degraded', `${alive} of ${members.length} nodes online`]

  return (
    <div className="shell">
      <a href="#main" className="skip-link">
        Skip to main content
      </a>
      <header className="topbar">
        <div className="brand">
          <svg viewBox="0 0 24 24" width="28" height="28" aria-hidden="true" focusable="false">
            <path d="M12 2l8 4v6c0 5-3.4 8.6-8 10-4.6-1.4-8-5-8-10V6l8-4z" fill="currentColor" />
            <path d="M8 12l3 3 5-6" fill="none" stroke="#fff" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
          <span>Vault</span>
          <span className="muted small">{app.zone}</span>
        </div>
        <StatusBadge health={health[0]} label={health[1]} />
      </header>
      <nav className="sidenav" aria-label="Main">
        <ul>
          {PAGES.filter((p) => !p.demoOnly || app.demoMode).map((p) => (
            <li key={p.id}>
              <button type="button" className="navlink" aria-current={page === p.id && !detail ? 'page' : undefined} onClick={() => go(p.id)}>
                {p.label}
              </button>
            </li>
          ))}
        </ul>
      </nav>
      <main id="main" ref={mainRef} tabIndex={-1} className="content">
        {detail ? (
          <ObjectDetail bucket={detail.bucket} objectKey={detail.key} demoMode={app.demoMode} onBack={() => setDetail(null)} />
        ) : page === 'scans' ? (
          <Scans onInspect={(bucket, key) => setDetail({ bucket, key })} />
        ) : page === 'buckets' ? (
          <Buckets />
        ) : page === 'cluster' ? (
          <Cluster app={app} status={status} />
        ) : page === 'activity' ? (
          <Activity events={events} status={status} />
        ) : (
          <Chaos app={app} status={status} />
        )}
      </main>
    </div>
  )
}
