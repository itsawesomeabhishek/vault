import { createReadStream, createWriteStream, statSync } from 'node:fs'
import { Readable, Transform } from 'node:stream'
import { pipeline } from 'node:stream/promises'
import type { ApiResponse, VaultEvent } from '../shared/types'

/** Talks to one local node's HTTP API with the bearer token. */
export class VaultClient {
  constructor(
    private readonly port: number,
    private readonly token: string
  ) {}

  private url(path: string): string {
    return `http://127.0.0.1:${this.port}${path}`
  }

  private headers(extra: Record<string, string> = {}): Record<string, string> {
    return { Authorization: `Bearer ${this.token}`, 'X-Vault-Actor': 'desktop-app', ...extra }
  }

  async request<T>(method: string, path: string, body?: unknown): Promise<ApiResponse<T>> {
    try {
      const res = await fetch(this.url(path), {
        method,
        headers: this.headers(body === undefined ? {} : { 'Content-Type': 'application/json' }),
        body: body === undefined ? undefined : JSON.stringify(body),
        signal: AbortSignal.timeout(method === 'POST' ? 600_000 : 30_000)
      })
      const text = await res.text()
      let data: unknown = null
      try {
        data = text ? JSON.parse(text) : null
      } catch {
        data = null
      }
      const error = res.ok ? null : ((data as { error?: string } | null)?.error ?? `HTTP ${res.status}`)
      return { ok: res.ok, status: res.status, data: res.ok ? (data as T) : null, error }
    } catch (err) {
      return { ok: false, status: 0, data: null, error: `Node unreachable: ${(err as Error).message}` }
    }
  }

  /** Streams a file from disk to the node without buffering it in memory. */
  async upload(
    bucket: string,
    key: string,
    filePath: string,
    contentType: string,
    metadata: Record<string, string>,
    onProgress: (sent: number, total: number) => void
  ): Promise<ApiResponse> {
    const total = statSync(filePath).size
    let sent = 0
    const counter = new Transform({
      transform(chunk: Buffer, _enc, cb) {
        sent += chunk.length
        onProgress(sent, total)
        cb(null, chunk)
      }
    })
    const body = Readable.toWeb(createReadStream(filePath).pipe(counter)) as ReadableStream
    const meta: Record<string, string> = {}
    for (const [k, v] of Object.entries(metadata)) meta[`X-Vault-Meta-${k}`] = encodeHeader(v)
    try {
      const res = await fetch(this.url(`/v1/buckets/${encodeURIComponent(bucket)}/objects/${encodeKey(key)}`), {
        method: 'PUT',
        headers: this.headers({ 'Content-Type': contentType, 'Content-Length': String(total), ...meta }),
        body,
        duplex: 'half'
      } as RequestInit & { duplex: 'half' })
      const data = (await res.json().catch(() => null)) as { error?: string } | null
      return { ok: res.ok, status: res.status, data, error: res.ok ? null : (data?.error ?? `HTTP ${res.status}`) }
    } catch (err) {
      return { ok: false, status: 0, data: null, error: (err as Error).message }
    }
  }

  /** Streams an object to a file; the node verifies every chunk's SHA-256 on the way. */
  async download(bucket: string, key: string, dest: string): Promise<ApiResponse> {
    try {
      const res = await fetch(this.url(`/v1/buckets/${encodeURIComponent(bucket)}/objects/${encodeKey(key)}`), {
        headers: this.headers()
      })
      if (!res.ok || !res.body) {
        const data = (await res.json().catch(() => null)) as { error?: string } | null
        return { ok: false, status: res.status, data: null, error: data?.error ?? `HTTP ${res.status}` }
      }
      await pipeline(Readable.fromWeb(res.body as never), createWriteStream(dest))
      return { ok: true, status: 200, data: null, error: null }
    } catch (err) {
      return { ok: false, status: 0, data: null, error: (err as Error).message }
    }
  }

  /** Subscribes to the node's server-sent events until the signal aborts. */
  async streamEvents(signal: AbortSignal, onEvent: (e: VaultEvent) => void): Promise<void> {
    const res = await fetch(this.url('/v1/events?history=50'), { headers: this.headers(), signal })
    if (!res.ok || !res.body) throw new Error(`events: HTTP ${res.status}`)
    const decoder = new TextDecoder()
    let buf = ''
    for await (const chunk of res.body as unknown as AsyncIterable<Uint8Array>) {
      buf += decoder.decode(chunk, { stream: true })
      let sep: number
      while ((sep = buf.indexOf('\n\n')) >= 0) {
        const block = buf.slice(0, sep)
        buf = buf.slice(sep + 2)
        const data = block
          .split('\n')
          .filter((l) => l.startsWith('data: '))
          .map((l) => l.slice(6))
          .join('')
        if (data) {
          try {
            onEvent(JSON.parse(data) as VaultEvent)
          } catch {
            // ignore malformed event
          }
        }
      }
    }
  }
}

export function encodeKey(key: string): string {
  return key.split('/').map(encodeURIComponent).join('/')
}

function encodeHeader(v: string): string {
  return v.replace(/[^\x20-\x7e]/g, '?').slice(0, 512)
}
