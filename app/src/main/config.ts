import { randomBytes } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import { hostname } from 'node:os'
import { join } from 'node:path'
import { app, safeStorage } from 'electron'
import { z } from 'zod'

/** Ports are fixed per local node so the installer can open matching firewall rules. */
export const NODE_PORTS = [
  { api: 18080, rpc: 19000, gossip: 17946 },
  { api: 18081, rpc: 19001, gossip: 17947 }
] as const

const configSchema = z.object({
  setupComplete: z.boolean(),
  zone: z.string(),
  storageDir: z.string(),
  maxGiB: z.number().nonnegative(),
  demoMode: z.boolean(),
  advertise: z.string(),
  apiTokenEnc: z.string(),
  pendingJoin: z.array(z.string().nullable())
})

export type Config = z.infer<typeof configSchema>

function configPath(): string {
  return join(app.getPath('userData'), 'vault-config.json')
}

export function defaultConfig(): Config {
  return {
    setupComplete: false,
    zone: hostname().toLowerCase().replace(/[^a-z0-9-]/g, '-').slice(0, 40) || 'this-pc',
    storageDir: join(app.getPath('userData'), 'data'),
    maxGiB: 20,
    demoMode: true,
    advertise: '',
    apiTokenEnc: '',
    pendingJoin: [null, null]
  }
}

export function loadConfig(): Config {
  const p = configPath()
  if (!existsSync(p)) return defaultConfig()
  try {
    return configSchema.parse(JSON.parse(readFileSync(p, 'utf8')))
  } catch {
    return defaultConfig()
  }
}

export function saveConfig(cfg: Config): void {
  mkdirSync(app.getPath('userData'), { recursive: true })
  writeFileSync(configPath(), JSON.stringify(cfg, null, 2), { mode: 0o600 })
}

/**
 * The API token authenticates the app to its local nodes. It is encrypted at
 * rest with the OS keychain (DPAPI on Windows) when available.
 */
export function apiToken(cfg: Config): string {
  if (cfg.apiTokenEnc) {
    try {
      const raw = Buffer.from(cfg.apiTokenEnc, 'base64')
      return safeStorage.isEncryptionAvailable() ? safeStorage.decryptString(raw) : raw.toString('utf8')
    } catch {
      // fall through and rotate
    }
  }
  const token = randomBytes(32).toString('hex')
  const enc = safeStorage.isEncryptionAvailable() ? safeStorage.encryptString(token) : Buffer.from(token, 'utf8')
  cfg.apiTokenEnc = enc.toString('base64')
  saveConfig(cfg)
  return token
}

export function nodeDataDir(cfg: Config, index: number): string {
  return join(cfg.storageDir, `node${index + 1}`)
}
