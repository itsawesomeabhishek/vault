import { spawn, type ChildProcess } from 'node:child_process'
import { EventEmitter } from 'node:events'
import { existsSync } from 'node:fs'
import { join } from 'node:path'
import { app } from 'electron'
import type { LocalNode, NodeRunState } from '../shared/types'

export interface NodeSpec {
  index: number
  dataDir: string
  zone: string
  apiPort: number
  rpcPort: number
  gossipPort: number
  maxGiB: number
  chaos: boolean
  advertise: string
  token: string
  /** One-shot bootstrap arguments (--init or --join CODE). Cleared after the first successful start. */
  bootstrap: string[]
}

const MAX_LOG_LINES = 300

export function nodeBinary(): string {
  const packaged = join(process.resourcesPath, 'bin', 'vault-node.exe')
  if (app.isPackaged && existsSync(packaged)) return packaged
  return join(app.getAppPath(), 'resources', 'bin', 'vault-node.exe')
}

/**
 * Runs one vault-node process, restarts it with exponential backoff if it
 * crashes, and exposes its state and recent logs.
 */
export class NodeProcess extends EventEmitter {
  private child: ChildProcess | null = null
  private wanted = false
  private backoffMs = 1000
  private restartTimer: NodeJS.Timeout | null = null
  readonly logs: string[] = []
  state: NodeRunState = 'stopped'
  id: string | null = null
  lastError: string | null = null
  restarts = 0

  constructor(public spec: NodeSpec) {
    super()
  }

  snapshot(): LocalNode {
    return {
      index: this.spec.index,
      id: this.id,
      state: this.state,
      apiPort: this.spec.apiPort,
      rpcPort: this.spec.rpcPort,
      gossipPort: this.spec.gossipPort,
      lastError: this.lastError,
      restarts: this.restarts
    }
  }

  /** Starts the node and resolves once it reports ready (or rejects on fatal error). */
  start(): Promise<string> {
    this.wanted = true
    return new Promise((resolve, reject) => {
      const onReady = (id: string): void => {
        cleanup()
        resolve(id)
      }
      const onFatal = (msg: string): void => {
        cleanup()
        reject(new Error(msg))
      }
      const cleanup = (): void => {
        this.off('ready', onReady)
        this.off('fatal', onFatal)
      }
      this.on('ready', onReady)
      this.on('fatal', onFatal)
      this.spawnChild()
    })
  }

  private args(): string[] {
    const s = this.spec
    const a = [
      '--data-dir', s.dataDir,
      '--zone', s.zone,
      '--rpc-port', String(s.rpcPort),
      '--gossip-port', String(s.gossipPort),
      '--api-addr', `127.0.0.1:${s.apiPort}`,
      '--max-gib', String(s.maxGiB),
      '--log-json'
    ]
    if (s.advertise) a.push('--advertise', s.advertise)
    if (s.chaos) a.push('--enable-chaos')
    return [...a, ...s.bootstrap]
  }

  private spawnChild(): void {
    if (this.child) return
    this.setState('starting')
    const child = spawn(nodeBinary(), this.args(), {
      env: { ...process.env, VAULT_API_TOKEN: this.spec.token },
      windowsHide: true,
      stdio: ['ignore', 'pipe', 'pipe']
    })
    this.child = child
    let buf = ''
    child.stdout?.setEncoding('utf8')
    child.stdout?.on('data', (chunk: string) => {
      buf += chunk
      let nl: number
      while ((nl = buf.indexOf('\n')) >= 0) {
        this.handleLine(buf.slice(0, nl).trim())
        buf = buf.slice(nl + 1)
      }
    })
    child.stderr?.setEncoding('utf8')
    child.stderr?.on('data', (chunk: string) => {
      for (const line of chunk.split('\n')) if (line.trim()) this.pushLog(line.trim())
    })
    child.on('error', (err) => {
      this.lastError = err.message
      this.emit('fatal', err.message)
    })
    child.on('exit', (code) => {
      this.child = null
      if (!this.wanted) {
        this.setState('stopped')
        return
      }
      this.setState('crashed')
      this.lastError = this.lastError ?? `exited with code ${code}`
      this.scheduleRestart()
    })
  }

  private handleLine(line: string): void {
    if (!line) return
    try {
      const msg = JSON.parse(line) as { event?: string; id?: string; error?: string }
      if (msg.event === 'ready' && msg.id) {
        this.id = msg.id
        this.lastError = null
        this.backoffMs = 1000
        this.spec.bootstrap = []
        this.setState('running')
        this.emit('ready', msg.id)
        return
      }
      if (msg.event === 'fatal') {
        this.lastError = msg.error ?? 'fatal error'
        this.emit('fatal', this.lastError)
        return
      }
    } catch {
      // not a supervisor message
    }
    this.pushLog(line)
  }

  private pushLog(line: string): void {
    this.logs.push(line)
    if (this.logs.length > MAX_LOG_LINES) this.logs.splice(0, this.logs.length - MAX_LOG_LINES)
  }

  private scheduleRestart(): void {
    if (this.restartTimer) return
    const delay = this.backoffMs
    this.backoffMs = Math.min(this.backoffMs * 2, 30_000)
    this.restartTimer = setTimeout(() => {
      this.restartTimer = null
      if (this.wanted) {
        this.restarts++
        this.spawnChild()
      }
    }, delay)
  }

  private setState(s: NodeRunState): void {
    this.state = s
    this.emit('state', this.snapshot())
  }

  /** Stops the node. Used for shutdown and for the "kill node" chaos action. */
  stop(): Promise<void> {
    this.wanted = false
    if (this.restartTimer) {
      clearTimeout(this.restartTimer)
      this.restartTimer = null
    }
    const child = this.child
    if (!child) {
      this.setState('stopped')
      return Promise.resolve()
    }
    return new Promise((resolve) => {
      child.once('exit', () => resolve())
      child.kill()
      setTimeout(() => resolve(), 5000)
    })
  }
}
