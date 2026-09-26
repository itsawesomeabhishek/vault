import type { ApiRequest, ApiResponse, AppState, SetupRequest, UploadProgress, UploadRequest, VaultEvent } from '../shared/types'

export interface VaultBridge {
  state(): Promise<AppState>
  setup(req: SetupRequest): Promise<ApiResponse<AppState>>
  api<T = unknown>(req: ApiRequest): Promise<ApiResponse<T>>
  upload(req: UploadRequest): Promise<ApiResponse>
  download(ref: { bucket: string; key: string }): Promise<ApiResponse>
  stopNode(index: number): Promise<AppState>
  startNode(index: number): Promise<ApiResponse<AppState>>
  nodeLogs(index: number): Promise<string[]>
  copy(text: string): Promise<boolean>
  pathForFile(file: File): string
  onEvent(cb: (e: VaultEvent) => void): () => void
  onState(cb: (s: AppState) => void): () => void
  onProgress(cb: (p: UploadProgress) => void): () => void
}

declare global {
  interface Window {
    vault: VaultBridge
  }
}
