import { randomUUID } from 'node:crypto'
import { mkdirSync, statSync } from 'node:fs'
import { basename, extname, join } from 'node:path'
import { app, BrowserWindow, clipboard, dialog, ipcMain, session, shell, type IpcMainInvokeEvent } from 'electron'
import type { ZodType } from 'zod'
import type { ApiResponse, AppState, UploadProgress, VaultEvent } from '../shared/types'
import { apiToken, loadConfig, NODE_PORTS, nodeDataDir, saveConfig, type Config } from './config'
import { VaultClient } from './client'
import { NodeProcess, type NodeSpec } from './supervisor'
import { apiRequestSchema, nodeIndexSchema, objectRefSchema, setupSchema, textSchema, uploadSchema } from './validation'

let win: BrowserWindow | null = null
let cfg: Config
let token = ''
const nodes: NodeProcess[] = []
const eventStreams: AbortController[] = []

const CONTENT_TYPES: Record<string, string> = {
  '.dcm': 'application/dicom',
  '.png': 'image/png',
  '.jpg': 'image/jpeg',
  '.jpeg': 'image/jpeg',
  '.pdf': 'application/pdf',
  '.txt': 'text/plain',
  '.json': 'application/json',
  '.zip': 'application/zip'
}

function send(channel: string, payload: unknown): void {
  if (win && !win.isDestroyed()) win.webContents.send(channel, payload)
}

function state(): AppState {
  return {
    setupComplete: cfg.setupComplete,
    zone: cfg.zone,
    storageDir: cfg.storageDir,
    demoMode: cfg.demoMode,
    advertise: cfg.advertise,
    nodes: nodes.map((n) => n.snapshot()),
    version: app.getVersion()
  }
}

function spec(index: number, bootstrap: string[]): NodeSpec {
  const ports = NODE_PORTS[index]!
  return {
    index,
    dataDir: nodeDataDir(cfg, index),
    zone: cfg.zone,
    apiPort: ports.api,
    rpcPort: ports.rpc,
    gossipPort: ports.gossip,
    maxGiB: cfg.maxGiB / NODE_PORTS.length,
    chaos: cfg.demoMode,
    advertise: cfg.advertise,
    token,
    bootstrap
  }
}

function ensureNode(index: number, bootstrap: string[]): NodeProcess {
  let n = nodes[index]
  if (!n) {
    n = new NodeProcess(spec(index, bootstrap))
    n.on('state', () => send('vault:state', state()))
    n.on('ready', () => startEventStream(index))
    nodes[index] = n
  } else {
    n.spec = { ...spec(index, bootstrap) }
  }
  return n
}

function clientFor(index?: number): VaultClient {
  const preferred = index ?? nodes.findIndex((n) => n?.state === 'running')
  const i = preferred >= 0 ? preferred : 0
  return new VaultClient(NODE_PORTS[i]!.api, token)
}

function startEventStream(index: number): void {
  eventStreams[index]?.abort()
  const ctrl = new AbortController()
  eventStreams[index] = ctrl
  const loop = async (): Promise<void> => {
    while (!ctrl.signal.aborted) {
      try {
        await clientFor(index).streamEvents(ctrl.signal, (e: VaultEvent) => send('vault:event', e))
      } catch {
        // node restarting; retry
      }
      await new Promise((r) => setTimeout(r, 2000))
    }
  }
  void loop()
}

/** Starts both local nodes. The second node always joins through the first with a fresh invite. */
async function startCluster(firstBootstrap: string[]): Promise<void> {
  mkdirSync(cfg.storageDir, { recursive: true })
  await ensureNode(0, firstBootstrap).start()
  cfg.pendingJoin[0] = null
  let second: string[] = []
  if (cfg.pendingJoin[1] === 'needed') {
    const inv = await clientFor(0).request<{ code: string }>('POST', '/v1/invites')
    if (!inv.ok || !inv.data) throw new Error(inv.error ?? 'could not create invite for second node')
    second = ['--join', inv.data.code]
  }
  await ensureNode(1, second).start()
  cfg.pendingJoin[1] = null
  saveConfig(cfg)
}

function handle<T>(channel: string, schema: ZodType<T> | null, fn: (arg: T, e: IpcMainInvokeEvent) => Promise<unknown> | unknown): void {
  ipcMain.handle(channel, async (e, raw: unknown) => {
    if (e.senderFrame?.url && !isTrustedUrl(e.senderFrame.url)) throw new Error('untrusted sender')
    const parsed = schema ? schema.safeParse(raw) : { success: true as const, data: raw as T }
    if (!parsed.success) {
      return { ok: false, status: 400, data: null, error: parsed.error.issues.map((i) => i.message).join('; ') } satisfies ApiResponse
    }
    return fn(parsed.data, e)
  })
}

function isTrustedUrl(url: string): boolean {
  return url.startsWith('file://') || (!!process.env['ELECTRON_RENDERER_URL'] && url.startsWith(process.env['ELECTRON_RENDERER_URL']))
}

function registerIpc(): void {
  handle('app:state', null, () => state())

  handle('setup:run', setupSchema, async (req) => {
    cfg.zone = req.zone.toLowerCase()
    cfg.maxGiB = req.maxGiB
    cfg.demoMode = req.demoMode
    cfg.advertise = req.advertise ?? ''
    cfg.pendingJoin = [null, 'needed']
    saveConfig(cfg)
    const boot = req.mode === 'create' ? ['--init'] : ['--join', (req.inviteCode ?? '').trim()]
    try {
      await startCluster(boot)
      cfg.setupComplete = true
      saveConfig(cfg)
      return { ok: true, status: 200, data: state(), error: null }
    } catch (err) {
      await Promise.all(nodes.map((n) => n?.stop()))
      return { ok: false, status: 500, data: null, error: (err as Error).message }
    }
  })

  handle('api:request', apiRequestSchema, (req) => clientFor(req.node).request(req.method, req.path, req.body))

  handle('object:upload', uploadSchema, async (req) => {
    let filePath = req.filePath
    if (!filePath) {
      const pick = await dialog.showOpenDialog(win!, { title: 'Choose a scan to upload', properties: ['openFile'] })
      if (pick.canceled || !pick.filePaths[0]) return { ok: false, status: 0, data: null, error: 'cancelled' }
      filePath = pick.filePaths[0]
    }
    try {
      if (!statSync(filePath).isFile()) throw new Error('not a file')
    } catch {
      return { ok: false, status: 400, data: null, error: 'The selected item is not a readable file.' }
    }
    const name = basename(filePath)
    const key = req.key ?? name
    const id = randomUUID()
    const progress: UploadProgress = { id, name, sent: 0, total: statSync(filePath).size, done: false, error: null }
    let last = 0
    const res = await clientFor().upload(req.bucket, key, filePath, CONTENT_TYPES[extname(name).toLowerCase()] ?? 'application/octet-stream', req.metadata, (sent, total) => {
      const now = Date.now()
      if (now - last > 100 || sent === total) {
        last = now
        send('vault:progress', { ...progress, sent, total })
      }
    })
    send('vault:progress', { ...progress, sent: progress.total, done: true, error: res.error })
    return res
  })

  handle('object:download', objectRefSchema, async (ref) => {
    const save = await dialog.showSaveDialog(win!, { title: 'Save scan', defaultPath: basename(ref.key) })
    if (save.canceled || !save.filePath) return { ok: false, status: 0, data: null, error: 'cancelled' }
    const res = await clientFor().download(ref.bucket, ref.key, save.filePath)
    if (res.ok) shell.showItemInFolder(save.filePath)
    return res
  })

  handle('node:stop', nodeIndexSchema, async ({ index }) => {
    await nodes[index]?.stop()
    eventStreams[index]?.abort()
    return state()
  })

  handle('node:start', nodeIndexSchema, async ({ index }) => {
    try {
      await ensureNode(index, []).start()
      return { ok: true, status: 200, data: state(), error: null }
    } catch (err) {
      return { ok: false, status: 500, data: null, error: (err as Error).message }
    }
  })

  handle('node:logs', nodeIndexSchema, ({ index }) => nodes[index]?.logs.slice(-200) ?? [])

  handle('clipboard:write', textSchema, ({ text }) => {
    clipboard.writeText(text)
    return true
  })
}

function createWindow(): void {
  win = new BrowserWindow({
    width: 1280,
    height: 860,
    minWidth: 960,
    minHeight: 640,
    title: 'Vault',
    show: false,
    backgroundColor: '#f6f8fb',
    webPreferences: {
      preload: join(__dirname, '../preload/index.js'),
      contextIsolation: true,
      nodeIntegration: false,
      sandbox: true,
      webSecurity: true,
      spellcheck: false
    }
  })
  win.setMenuBarVisibility(false)
  win.once('ready-to-show', () => win?.show())
  win.webContents.setWindowOpenHandler(() => ({ action: 'deny' }))
  win.webContents.on('will-navigate', (e, url) => {
    if (!isTrustedUrl(url)) e.preventDefault()
  })
  if (process.env['ELECTRON_RENDERER_URL']) void win.loadURL(process.env['ELECTRON_RENDERER_URL'])
  else void win.loadFile(join(__dirname, '../renderer/index.html'))
}

if (!app.requestSingleInstanceLock()) {
  app.quit()
} else {
  app.on('second-instance', () => {
    if (win) {
      if (win.isMinimized()) win.restore()
      win.focus()
    }
  })

  app.whenReady().then(async () => {
    if (process.env['VAULT_USER_DATA']) app.setPath('userData', process.env['VAULT_USER_DATA'])
    session.defaultSession.setPermissionRequestHandler((_wc, _perm, cb) => cb(false))
    cfg = loadConfig()
    token = apiToken(cfg)
    registerIpc()
    createWindow()
    if (cfg.setupComplete) {
      const firstBoot = cfg.pendingJoin[0] ? ['--join', cfg.pendingJoin[0]] : []
      startCluster(firstBoot).catch((err: Error) => send('vault:event', {
        id: Date.now(), time: new Date().toISOString(), type: 'app.error', severity: 'error', node: 'app', message: err.message
      } satisfies VaultEvent))
    }
  })

  let quitting = false
  app.on('before-quit', (e) => {
    if (quitting) return
    quitting = true
    e.preventDefault()
    eventStreams.forEach((c) => c?.abort())
    void Promise.all(nodes.map((n) => n?.stop())).finally(() => app.quit())
  })

  app.on('window-all-closed', () => app.quit())
}
