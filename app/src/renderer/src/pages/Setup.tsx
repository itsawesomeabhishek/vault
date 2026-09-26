import { useId, useState, type FormEvent } from 'react'
import type { AppState, SetupRequest } from '../../../shared/types'

export function Setup({ initial, onDone }: { initial: AppState; onDone: (s: AppState) => void }): JSX.Element {
  const [mode, setMode] = useState<SetupRequest['mode']>('create')
  const [zone, setZone] = useState(initial.zone)
  const [invite, setInvite] = useState('')
  const [maxGiB, setMaxGiB] = useState(20)
  const [demoMode, setDemoMode] = useState(true)
  const [advertise, setAdvertise] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const ids = { zone: useId(), invite: useId(), max: useId(), adv: useId(), err: useId(), create: useId(), join: useId() }

  const submit = async (e: FormEvent): Promise<void> => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const res = await window.vault.setup({ mode, zone, inviteCode: mode === 'join' ? invite : undefined, maxGiB, demoMode, advertise })
    setBusy(false)
    if (res.ok && res.data) onDone(res.data)
    else setError(res.error ?? 'Setup failed')
  }

  return (
    <main id="main" className="setup">
      <h1>Welcome to Vault</h1>
      <p className="lead">
        Vault keeps every medical scan on several computers at once, checks it for damage, and repairs it automatically. This
        computer will run two storage nodes.
      </p>
      <form onSubmit={submit} aria-describedby={error ? ids.err : undefined} className="card">
        <fieldset>
          <legend>How do you want to start?</legend>
          <label className="choice" htmlFor={ids.create}>
            <input id={ids.create} type="radio" name="mode" value="create" checked={mode === 'create'} onChange={() => setMode('create')} />
            <span>
              <strong>Create a new cluster</strong>
              <span className="muted">Choose this on the first computer.</span>
            </span>
          </label>
          <label className="choice" htmlFor={ids.join}>
            <input id={ids.join} type="radio" name="mode" value="join" checked={mode === 'join'} onChange={() => setMode('join')} />
            <span>
              <strong>Join an existing cluster</strong>
              <span className="muted">Paste the invite code shown on the other computer (Cluster page, then Create invite).</span>
            </span>
          </label>
        </fieldset>

        {mode === 'join' && (
          <div className="field">
            <label htmlFor={ids.invite}>Invite code</label>
            <textarea id={ids.invite} required rows={3} value={invite} onChange={(e) => setInvite(e.target.value)} spellCheck={false} placeholder="vault1.…" />
          </div>
        )}

        <div className="field">
          <label htmlFor={ids.zone}>Name of this computer (failure zone)</label>
          <input id={ids.zone} required pattern="[a-zA-Z0-9-]{1,40}" value={zone} onChange={(e) => setZone(e.target.value)} aria-describedby={`${ids.zone}-hint`} />
          <p id={`${ids.zone}-hint`} className="hint">
            Vault always keeps copies on more than one computer. Letters, numbers and hyphens.
          </p>
        </div>

        <div className="field">
          <label htmlFor={ids.max}>Storage limit on this computer (GB)</label>
          <input id={ids.max} type="number" min={1} max={100000} required value={maxGiB} onChange={(e) => setMaxGiB(Number(e.target.value))} />
        </div>

        <details>
          <summary>Advanced network settings</summary>
          <div className="field">
            <label htmlFor={ids.adv}>Address other computers use to reach this one</label>
            <input id={ids.adv} value={advertise} onChange={(e) => setAdvertise(e.target.value)} placeholder="Automatic (Tailscale address preferred)" aria-describedby={`${ids.adv}-hint`} />
            <p id={`${ids.adv}-hint`} className="hint">
              Leave blank. On different networks, install Tailscale on both computers and Vault will use it automatically.
            </p>
          </div>
        </details>

        <label className="check">
          <input type="checkbox" checked={demoMode} onChange={(e) => setDemoMode(e.target.checked)} />
          <span>Enable demo tools (simulate corruption, network partitions and node crashes)</span>
        </label>

        {error && (
          <p id={ids.err} role="alert" className="error-text">
            {error}
          </p>
        )}
        <div className="row end">
          <button type="submit" className="btn btn-primary" disabled={busy} aria-busy={busy}>
            {busy ? 'Starting storage nodes…' : mode === 'create' ? 'Create cluster' : 'Join cluster'}
          </button>
        </div>
      </form>
    </main>
  )
}
