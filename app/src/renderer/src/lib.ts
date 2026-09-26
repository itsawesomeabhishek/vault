import { useCallback, useEffect, useRef, useState } from 'react'
import type { ApiRequest, ApiResponse, AppState, NodeStatus, VaultEvent } from '../../shared/types'

export function api<T>(method: ApiRequest['method'], path: string, body?: unknown, node?: number): Promise<ApiResponse<T>> {
  return window.vault.api<T>({ method, path, body, node })
}

export function objectPath(bucket: string, key: string): string {
  return `/v1/buckets/${bucket}/objects/${key.split('/').map(encodeURIComponent).join('/')}`
}

export function inspectPath(bucket: string, key: string): string {
  return `/v1/buckets/${bucket}/inspect/${key.split('/').map(encodeURIComponent).join('/')}`
}

/** Polls a GET endpoint. Pauses while the window is hidden to save work. */
export function usePoll<T>(path: string | null, intervalMs: number): { data: T | null; error: string | null; refresh: () => void } {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)
  const refresh = useCallback(() => setTick((t) => t + 1), [])
  useEffect(() => {
    if (!path) return
    let alive = true
    const load = async (): Promise<void> => {
      if (document.hidden) return
      const res = await api<T>('GET', path)
      if (!alive) return
      if (res.ok) {
        setData(res.data)
        setError(null)
      } else {
        setError(res.error)
      }
    }
    void load()
    const t = setInterval(load, intervalMs)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [path, intervalMs, tick])
  return { data, error, refresh }
}

export function useStatus(): { status: NodeStatus | null; error: string | null } {
  const { data, error } = usePoll<NodeStatus>('/v1/status', 2000)
  return { status: data, error }
}

export function useAppState(): [AppState | null, (s: AppState) => void] {
  const [state, setState] = useState<AppState | null>(null)
  useEffect(() => {
    void window.vault.state().then(setState)
    return window.vault.onState(setState)
  }, [])
  return [state, setState]
}

const MAX_EVENTS = 300

export function useEvents(): VaultEvent[] {
  const [events, setEvents] = useState<VaultEvent[]>([])
  const seen = useRef(new Set<string>())
  useEffect(
    () =>
      window.vault.onEvent((e) => {
        const k = `${e.node}:${e.id}:${e.time}`
        if (seen.current.has(k)) return
        seen.current.add(k)
        setEvents((prev) => [e, ...prev].slice(0, MAX_EVENTS))
      }),
    []
  )
  return events
}

export function formatTime(iso: string): string {
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleString()
}
