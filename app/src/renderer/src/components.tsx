import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'

export type Health = 'healthy' | 'degraded' | 'down' | 'unknown' | 'working'

const ICONS: Record<Health, ReactNode> = {
  healthy: <path d="M4 10.5l4 4 8-9" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" strokeLinejoin="round" />,
  degraded: <path d="M10 3l8 14H2L10 3zm0 5v4m0 2.5v.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinejoin="round" />,
  down: <path d="M5 5l10 10M15 5L5 15" fill="none" stroke="currentColor" strokeWidth="2.5" strokeLinecap="round" />,
  unknown: <path d="M7.5 7.5a2.5 2.5 0 115 0c0 2-2.5 2-2.5 4m0 3v.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />,
  working: <circle cx="10" cy="10" r="6" fill="none" stroke="currentColor" strokeWidth="2.5" strokeDasharray="20 20" />
}

/** Status is always conveyed by icon shape and text, never by colour alone. */
export function StatusBadge({ health, label }: { health: Health; label: string }): JSX.Element {
  return (
    <span className={`badge badge-${health}`}>
      <svg viewBox="0 0 20 20" width="16" height="16" aria-hidden="true" focusable="false">
        {ICONS[health]}
      </svg>
      <span>{label}</span>
    </span>
  )
}

export function ProgressBar({ value, max, label }: { value: number; max: number; label: string }): JSX.Element {
  const pct = max > 0 ? Math.min(100, Math.round((value / max) * 100)) : 0
  return (
    <div className="progress">
      <div
        role="progressbar"
        aria-label={label}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct}
        aria-valuetext={`${pct}%`}
        className="progress-track"
      >
        <div className="progress-fill" style={{ width: `${pct}%` }} />
      </div>
      <span className="progress-text" aria-hidden="true">
        {pct}%
      </span>
    </div>
  )
}

type Tone = 'info' | 'success' | 'error'
interface Notice {
  id: number
  tone: Tone
  text: string
}

const NoticeContext = createContext<(text: string, tone?: Tone) => void>(() => undefined)

export function useNotify(): (text: string, tone?: Tone) => void {
  return useContext(NoticeContext)
}

/** Announces results to screen readers (polite status / assertive alert) and shows them visually. */
export function NoticeProvider({ children }: { children: ReactNode }): JSX.Element {
  const [notices, setNotices] = useState<Notice[]>([])
  const nextId = useRef(1)
  const notify = useCallback((text: string, tone: Tone = 'info') => {
    const id = nextId.current++
    setNotices((n) => [...n, { id, tone, text }].slice(-4))
    setTimeout(() => setNotices((n) => n.filter((x) => x.id !== id)), tone === 'error' ? 10000 : 5000)
  }, [])
  return (
    <NoticeContext.Provider value={notify}>
      {children}
      <div className="notices" role="status" aria-live="polite">
        {notices
          .filter((n) => n.tone !== 'error')
          .map((n) => (
            <p key={n.id} className={`notice notice-${n.tone}`}>
              {n.text}
            </p>
          ))}
      </div>
      {notices.some((n) => n.tone === 'error') && (
        <div className="notices notices-errors" role="alert">
          {notices
            .filter((n) => n.tone === 'error')
            .map((n) => (
              <p key={n.id} className="notice notice-error">
                {n.text}
              </p>
            ))}
        </div>
      )}
    </NoticeContext.Provider>
  )
}

/** Native modal dialog: traps focus, closes on Escape, restores focus on close. */
export function ConfirmDialog(props: {
  open: boolean
  title: string
  body: string
  confirmLabel: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
}): JSX.Element {
  const ref = useRef<HTMLDialogElement>(null)
  useEffect(() => {
    const d = ref.current
    if (!d) return
    if (props.open && !d.open) d.showModal?.()
    if (!props.open && d.open) d.close?.()
  }, [props.open])
  return (
    <dialog ref={ref} aria-labelledby="confirm-title" aria-describedby="confirm-body" onCancel={props.onCancel} className="dialog">
      <h2 id="confirm-title">{props.title}</h2>
      <p id="confirm-body">{props.body}</p>
      <div className="row end">
        <button type="button" className="btn" onClick={props.onCancel}>
          Cancel
        </button>
        <button type="button" className={props.danger ? 'btn btn-danger' : 'btn btn-primary'} onClick={props.onConfirm}>
          {props.confirmLabel}
        </button>
      </div>
    </dialog>
  )
}

export function Card({ title, children, actions, id }: { title: string; children: ReactNode; actions?: ReactNode; id?: string }): JSX.Element {
  const headingId = `${id ?? title.replace(/\W+/g, '-').toLowerCase()}-heading`
  return (
    <section className="card" aria-labelledby={headingId}>
      <div className="card-head">
        <h2 id={headingId}>{title}</h2>
        {actions && <div className="row">{actions}</div>}
      </div>
      {children}
    </section>
  )
}

export function Stat({ label, value, hint }: { label: string; value: ReactNode; hint?: string }): JSX.Element {
  return (
    <div className="stat">
      <dt>{label}</dt>
      <dd>
        {value}
        {hint && <span className="stat-hint">{hint}</span>}
      </dd>
    </div>
  )
}
