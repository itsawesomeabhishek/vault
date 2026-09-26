// Types shared by the main process, preload and renderer.

export type NodeRunState = 'stopped' | 'starting' | 'running' | 'crashed'

export interface LocalNode {
  index: number
  id: string | null
  state: NodeRunState
  apiPort: number
  rpcPort: number
  gossipPort: number
  lastError: string | null
  restarts: number
}

export interface AppState {
  setupComplete: boolean
  zone: string
  storageDir: string
  demoMode: boolean
  advertise: string
  nodes: LocalNode[]
  version: string
}

export interface SetupRequest {
  mode: 'create' | 'join'
  zone: string
  inviteCode?: string
  maxGiB: number
  demoMode: boolean
  advertise?: string
}

export interface ApiRequest {
  node?: number
  method: 'GET' | 'PUT' | 'POST' | 'DELETE'
  path: string
  body?: unknown
}

export interface ApiResponse<T = unknown> {
  ok: boolean
  status: number
  data: T | null
  error: string | null
}

export interface UploadRequest {
  bucket: string
  key?: string
  filePath?: string
  metadata: Record<string, string>
}

export interface UploadProgress {
  id: string
  name: string
  sent: number
  total: number
  done: boolean
  error: string | null
}

export interface VaultEvent {
  id: number
  time: string
  type: string
  severity: 'info' | 'warning' | 'error'
  node: string
  bucket?: string
  key?: string
  message: string
}

export interface Policy {
  n: number
  k: number
  w: number
  r: number
  sloppy: boolean
}

export interface Bucket {
  name: string
  policy: Policy
  description: string
  overhead: number
  faultTolerance: number
  created: string
}

export interface ObjectSummary {
  bucket: string
  key: string
  size: number
  contentType: string
  etag: string
  metadata?: Record<string, string>
  modified: string
  policy: string
}

export interface SlotStatus {
  slot: number
  node: string
  zone: string
  alive: boolean
  hasManifest: boolean
  versionMatch: boolean
  shardsPresent: number
  shardsTotal: number
  healthy: boolean
  error?: string
}

export interface Inspection {
  object: ObjectSummary
  width: number
  dataShards: number
  healthySlots: number
  readable: boolean
  overhead: number
  faultTolerance: number
  slots: SlotStatus[]
  extraHolders: string[] | null
}

export interface MemberStatus {
  id: string
  zone: string
  rpcAddr: string
  apiAddr?: string
  state: 'alive' | 'suspect' | 'dead' | 'left' | 'unknown'
  share: number
  self: boolean
  since: string
}

export interface LatencySummary {
  count: number
  meanMs: number
  p50Ms: number
  p99Ms: number
}

export interface NodeStatus {
  id: string
  zone: string
  usedBytes: number
  maxBytes: number
  objects: number
  tombstones: number
  logicalBytes: number
  hints: number
  repairQueue: number
  members: MemberStatus[]
  partitionedFrom: string[] | null
  counters: Record<string, number>
  latencies: Record<string, LatencySummary>
  generatedAt: string
  encryptAtRest: boolean
}
