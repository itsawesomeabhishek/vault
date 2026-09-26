import { useEffect, useId, useMemo, useState, type DragEvent, type FormEvent } from 'react'
import type { Bucket, ObjectSummary, UploadProgress } from '../../../shared/types'
import { formatBytes } from '../../../shared/policy'
import { Card, ConfirmDialog, ProgressBar, useNotify } from '../components'
import { api, formatTime, objectPath, usePoll } from '../lib'

const MODALITIES = ['CT', 'MRI', 'X-ray', 'Ultrasound', 'PET', 'Report', 'Other']

export function Scans({ onInspect }: { onInspect: (bucket: string, key: string) => void }): JSX.Element {
  const notify = useNotify()
  const { data: buckets } = usePoll<Bucket[]>('/v1/buckets', 5000)
  const [bucket, setBucket] = useState('scans')
  const [patient, setPatient] = useState('')
  const [modality, setModality] = useState('CT')
  const [studyDate, setStudyDate] = useState(new Date().toISOString().slice(0, 10))
  const [search, setSearch] = useState('')
  const [progress, setProgress] = useState<Record<string, UploadProgress>>({})
  const [dragOver, setDragOver] = useState(false)
  const [pendingDelete, setPendingDelete] = useState<ObjectSummary | null>(null)
  const ids = { bucket: useId(), patient: useId(), modality: useId(), date: useId(), search: useId() }

  const listPath = useMemo(() => {
    const q = search.trim() ? `?patient=${encodeURIComponent(search.trim())}` : ''
    return `/v1/buckets/${bucket}/objects${q}`
  }, [bucket, search])
  const bucketList = Array.isArray(buckets) ? buckets : []
  const { data: listing, error, refresh } = usePoll<{ items: ObjectSummary[] }>(bucketList.some((b) => b.name === bucket) ? listPath : null, 3000)

  useEffect(() => {
    if (!Array.isArray(buckets) || buckets.length === 0) return
    if (!buckets.some((b) => b.name === bucket)) setBucket(buckets[0]!.name)
  }, [buckets, bucket])

  useEffect(
    () =>
      window.vault.onProgress((p) => {
        setProgress((prev) => ({ ...prev, [p.id]: p }))
        if (p.done) {
          setTimeout(() => setProgress((prev) => Object.fromEntries(Object.entries(prev).filter(([k]) => k !== p.id))), 4000)
          if (p.error) notify(`Upload of ${p.name} failed: ${p.error}`, 'error')
          else {
            notify(`${p.name} stored and replicated`, 'success')
            refresh()
          }
        }
      }),
    [notify, refresh]
  )

  const metadata = (): Record<string, string> | null => {
    if (!patient.trim()) {
      notify('Enter a patient ID before uploading.', 'error')
      document.getElementById(ids.patient)?.focus()
      return null
    }
    return { 'patient-id': patient.trim(), modality, 'study-date': studyDate }
  }

  const upload = async (filePath?: string, name?: string): Promise<void> => {
    const meta = metadata()
    if (!meta) return
    const fileName = name ?? (filePath ? filePath.split(/[\\/]/).pop() : undefined)
    const key = fileName ? `patients/${patient.trim()}/${studyDate}/${fileName}` : undefined
    const res = await window.vault.upload({ bucket, filePath, metadata: meta, key })
    if (!res.ok && res.error && res.error !== 'cancelled' && !Object.keys(progress).length) notify(res.error, 'error')
  }

  const pick = async (e: FormEvent): Promise<void> => {
    e.preventDefault()
    const meta = metadata()
    if (!meta) return
    const res = await window.vault.upload({ bucket, metadata: meta })
    if (!res.ok && res.error && res.error !== 'cancelled') notify(res.error, 'error')
  }

  const onDrop = (e: DragEvent): void => {
    e.preventDefault()
    setDragOver(false)
    for (const f of Array.from(e.dataTransfer.files)) void upload(window.vault.pathForFile(f), f.name)
  }

  const download = async (o: ObjectSummary): Promise<void> => {
    const res = await window.vault.download({ bucket: o.bucket, key: o.key })
    if (res.ok) notify(`Saved ${o.key.split('/').pop()} (checksum verified)`, 'success')
    else if (res.error !== 'cancelled') notify(`Download failed: ${res.error}`, 'error')
  }

  const confirmDelete = async (): Promise<void> => {
    const o = pendingDelete
    setPendingDelete(null)
    if (!o) return
    const res = await api('DELETE', objectPath(o.bucket, o.key))
    notify(res.ok ? `Deleted ${o.key}` : `Delete failed: ${res.error}`, res.ok ? 'success' : 'error')
    refresh()
  }

  const items = listing?.items ?? []

  return (
    <>
      <h1>Scans</h1>
      <Card title="Upload a scan">
        <form onSubmit={pick} className="grid-form">
          <div className="field">
            <label htmlFor={ids.patient}>Patient ID</label>
            <input id={ids.patient} value={patient} onChange={(e) => setPatient(e.target.value)} placeholder="e.g. P-1042" autoComplete="off" required />
          </div>
          <div className="field">
            <label htmlFor={ids.modality}>Modality</label>
            <select id={ids.modality} value={modality} onChange={(e) => setModality(e.target.value)}>
              {MODALITIES.map((m) => (
                <option key={m}>{m}</option>
              ))}
            </select>
          </div>
          <div className="field">
            <label htmlFor={ids.date}>Study date</label>
            <input id={ids.date} type="date" value={studyDate} onChange={(e) => setStudyDate(e.target.value)} required />
          </div>
          <div className="field">
            <label htmlFor={ids.bucket}>Storage policy (bucket)</label>
            <select id={ids.bucket} value={bucket} onChange={(e) => setBucket(e.target.value)}>
              {(buckets ?? []).map((b) => (
                <option key={b.name} value={b.name}>
                  {b.name} — {b.description}
                </option>
              ))}
            </select>
          </div>
          <div
            className={`dropzone${dragOver ? ' dropzone-active' : ''}`}
            onDragOver={(e) => {
              e.preventDefault()
              setDragOver(true)
            }}
            onDragLeave={() => setDragOver(false)}
            onDrop={onDrop}
          >
            <p>Drag files here, or</p>
            <button type="submit" className="btn btn-primary">
              Choose file…
            </button>
          </div>
        </form>
        {Object.values(progress).length > 0 && (
          <ul className="uploads" aria-label="Uploads in progress">
            {Object.values(progress).map((p) => (
              <li key={p.id}>
                <span>{p.name}</span>
                <ProgressBar value={p.sent} max={p.total} label={`Uploading ${p.name}`} />
                <span className="muted">{p.done ? (p.error ? 'Failed' : 'Stored') : `${formatBytes(p.sent)} of ${formatBytes(p.total)}`}</span>
              </li>
            ))}
          </ul>
        )}
      </Card>

      <Card
        title="Stored scans"
        actions={
          <div className="field inline">
            <label htmlFor={ids.search}>Search by patient ID</label>
            <input id={ids.search} type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="P-1042" />
          </div>
        }
      >
        {error && (
          <p role="alert" className="error-text">
            Could not load scans: {error}
          </p>
        )}
        {items.length === 0 ? (
          <p className="muted">{search ? 'No scans for this patient.' : 'No scans stored yet.'}</p>
        ) : (
          <div className="table-wrap">
            <table>
              <caption className="sr-only">Scans in bucket {bucket}</caption>
              <thead>
                <tr>
                  <th scope="col">File</th>
                  <th scope="col">Patient</th>
                  <th scope="col">Modality</th>
                  <th scope="col">Size</th>
                  <th scope="col">Stored</th>
                  <th scope="col">
                    <span className="sr-only">Actions</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {items.map((o) => (
                  <tr key={o.key}>
                    <th scope="row" className="mono">
                      {o.key}
                    </th>
                    <td>{o.metadata?.['patient-id'] ?? '—'}</td>
                    <td>{o.metadata?.['modality'] ?? '—'}</td>
                    <td>{formatBytes(o.size)}</td>
                    <td>{formatTime(o.modified)}</td>
                    <td className="row">
                      <button type="button" className="btn btn-small" onClick={() => void download(o)} aria-label={`Download ${o.key}`}>
                        Download
                      </button>
                      <button type="button" className="btn btn-small" onClick={() => onInspect(o.bucket, o.key)} aria-label={`Show replicas of ${o.key}`}>
                        Replicas
                      </button>
                      <button type="button" className="btn btn-small btn-danger" onClick={() => setPendingDelete(o)} aria-label={`Delete ${o.key}`}>
                        Delete
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
      <ConfirmDialog
        open={pendingDelete !== null}
        title="Delete scan?"
        body={`${pendingDelete?.key ?? ''} will be deleted from every node in the cluster.`}
        confirmLabel="Delete"
        danger
        onConfirm={() => void confirmDelete()}
        onCancel={() => setPendingDelete(null)}
      />
    </>
  )
}
