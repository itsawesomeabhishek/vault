import { useId, useState, type ChangeEvent, type FormEvent } from 'react'
import type { Bucket, Policy } from '../../../shared/types'
import { ackFaultTolerance, describePolicy, faultTolerance, overhead, validatePolicy } from '../../../shared/policy'
import { Card, useNotify } from '../components'
import { api, usePoll } from '../lib'

export function Buckets(): JSX.Element {
  const notify = useNotify()
  const { data: buckets, refresh } = usePoll<Bucket[]>('/v1/buckets', 5000)
  const [name, setName] = useState('')
  const [kind, setKind] = useState<'replicated' | 'erasure'>('replicated')
  const [p, setP] = useState<Policy>({ n: 3, k: 1, w: 2, r: 2, sloppy: true })
  const ids = { name: useId(), n: useId(), k: useId(), w: useId(), r: useId(), errs: useId(), repl: useId(), ec: useId() }
  const policy: Policy = kind === 'replicated' ? { ...p, k: 1 } : p
  const errors = validatePolicy(policy)

  const setField = (f: keyof Policy) => (e: ChangeEvent<HTMLInputElement>) =>
    setP((prev) => ({ ...prev, [f]: f === 'sloppy' ? e.target.checked : Number(e.target.value) }))

  const create = async (e: FormEvent): Promise<void> => {
    e.preventDefault()
    if (errors.length) return
    const res = await api('PUT', `/v1/buckets/${name}`, { policy })
    notify(res.ok ? `Bucket ${name} created` : `Could not create bucket: ${res.error}`, res.ok ? 'success' : 'error')
    if (res.ok) {
      setName('')
      refresh()
    }
  }

  return (
    <>
      <h1>Buckets and durability policies</h1>
      <Card title="Buckets">
        <div className="table-wrap">
          <table>
            <caption className="sr-only">Buckets</caption>
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Policy</th>
                <th scope="col">Overhead</th>
                <th scope="col">Survives losing</th>
                <th scope="col">Write / read quorum</th>
                <th scope="col">When owners are down</th>
              </tr>
            </thead>
            <tbody>
              {(buckets ?? []).map((b) => (
                <tr key={b.name}>
                  <th scope="row">{b.name}</th>
                  <td>{b.policy.k > 1 ? `Erasure ${b.policy.k}+${b.policy.n - b.policy.k}` : `Replicated x${b.policy.n}`}</td>
                  <td>{b.overhead.toFixed(2)}x</td>
                  <td>{b.faultTolerance} node(s)</td>
                  <td>
                    W={b.policy.w}, R={b.policy.r}
                  </td>
                  <td>{b.policy.sloppy ? 'Stay writable (hinted handoff)' : 'Refuse writes (strict)'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </Card>

      <Card title="Create a bucket">
        <form onSubmit={create} aria-describedby={errors.length ? ids.errs : undefined}>
          <div className="field">
            <label htmlFor={ids.name}>Bucket name</label>
            <input id={ids.name} value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z0-9][a-z0-9-]{1,61}[a-z0-9]" required aria-describedby={`${ids.name}-hint`} />
            <p id={`${ids.name}-hint`} className="hint">
              3–63 lowercase letters, numbers or hyphens.
            </p>
          </div>
          <fieldset>
            <legend>Protection type</legend>
            <label className="choice" htmlFor={ids.repl}>
              <input id={ids.repl} type="radio" name="kind" checked={kind === 'replicated'} onChange={() => setKind('replicated')} />
              <span>
                <strong>Full copies (replication)</strong>
                <span className="muted">Fastest reads and repairs, more disk space.</span>
              </span>
            </label>
            <label className="choice" htmlFor={ids.ec}>
              <input
                id={ids.ec}
                type="radio"
                name="kind"
                checked={kind === 'erasure'}
                onChange={() => {
                  setKind('erasure')
                  setP({ n: 3, k: 2, w: 2, r: 2, sloppy: true })
                }}
              />
              <span>
                <strong>Erasure coding</strong>
                <span className="muted">Splits each chunk into data and parity shards. Much less disk space.</span>
              </span>
            </label>
          </fieldset>
          <div className="grid-form">
            <div className="field">
              <label htmlFor={ids.n}>{kind === 'replicated' ? 'Copies (N)' : 'Total shards (N)'}</label>
              <input id={ids.n} type="number" min={1} max={16} value={p.n} onChange={setField('n')} />
            </div>
            {kind === 'erasure' && (
              <div className="field">
                <label htmlFor={ids.k}>Data shards (K)</label>
                <input id={ids.k} type="number" min={2} max={15} value={p.k} onChange={setField('k')} />
              </div>
            )}
            <div className="field">
              <label htmlFor={ids.w}>Write quorum (W)</label>
              <input id={ids.w} type="number" min={1} max={16} value={p.w} onChange={setField('w')} />
            </div>
            <div className="field">
              <label htmlFor={ids.r}>Read quorum (R)</label>
              <input id={ids.r} type="number" min={1} max={16} value={p.r} onChange={setField('r')} />
            </div>
          </div>
          <label className="check">
            <input type="checkbox" checked={p.sloppy} onChange={setField('sloppy')} />
            <span>Keep accepting writes when an owner is offline (sloppy quorum with hinted handoff)</span>
          </label>
          <div className="summary" aria-live="polite">
            {errors.length === 0 ? (
              <>
                <p>
                  <strong>{describePolicy(policy)}.</strong>
                </p>
                <p className="muted">
                  Uses {overhead(policy).toFixed(2)} GB of disk per GB stored. Data stays readable with up to {faultTolerance(policy)} node(s) lost; right after a write is acknowledged it already survives {ackFaultTolerance(policy)} loss(es).
                </p>
              </>
            ) : (
              <ul id={ids.errs} className="error-text">
                {errors.map((e) => (
                  <li key={e}>{e}</li>
                ))}
              </ul>
            )}
          </div>
          <div className="row end">
            <button type="submit" className="btn btn-primary" disabled={errors.length > 0}>
              Create bucket
            </button>
          </div>
        </form>
      </Card>
    </>
  )
}
