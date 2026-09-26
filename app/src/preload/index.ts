import { contextBridge, ipcRenderer, webUtils, type IpcRendererEvent } from 'electron'
import type { VaultBridge } from './api'

function subscribe<T>(channel: string, cb: (payload: T) => void): () => void {
  const listener = (_e: IpcRendererEvent, payload: T): void => cb(payload)
  ipcRenderer.on(channel, listener)
  return () => ipcRenderer.removeListener(channel, listener)
}

const bridge: VaultBridge = {
  state: () => ipcRenderer.invoke('app:state'),
  setup: (req) => ipcRenderer.invoke('setup:run', req),
  api: (req) => ipcRenderer.invoke('api:request', req),
  upload: (req) => ipcRenderer.invoke('object:upload', req),
  download: (ref) => ipcRenderer.invoke('object:download', ref),
  stopNode: (index) => ipcRenderer.invoke('node:stop', { index }),
  startNode: (index) => ipcRenderer.invoke('node:start', { index }),
  nodeLogs: (index) => ipcRenderer.invoke('node:logs', { index }),
  copy: (text) => ipcRenderer.invoke('clipboard:write', { text }),
  pathForFile: (file) => webUtils.getPathForFile(file),
  onEvent: (cb) => subscribe('vault:event', cb),
  onState: (cb) => subscribe('vault:state', cb),
  onProgress: (cb) => subscribe('vault:progress', cb)
}

contextBridge.exposeInMainWorld('vault', bridge)
