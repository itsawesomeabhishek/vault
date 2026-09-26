import '@testing-library/jest-dom/vitest'
import { vi } from 'vitest'
import type { VaultBridge } from '../../src/preload/api'

const noop = (): (() => void) => () => undefined

export function installBridge(overrides: Partial<VaultBridge> = {}): VaultBridge {
  const bridge: VaultBridge = {
    state: vi.fn(),
    setup: vi.fn(),
    api: vi.fn().mockResolvedValue({ ok: true, status: 200, data: [], error: null }),
    upload: vi.fn(),
    download: vi.fn(),
    stopNode: vi.fn(),
    startNode: vi.fn(),
    nodeLogs: vi.fn(),
    copy: vi.fn(),
    pathForFile: vi.fn(),
    onEvent: noop,
    onState: noop,
    onProgress: noop,
    ...overrides
  } as VaultBridge
  window.vault = bridge
  return bridge
}

installBridge()
HTMLDialogElement.prototype.showModal ??= function (this: HTMLDialogElement) {
  this.open = true
}
HTMLDialogElement.prototype.close ??= function (this: HTMLDialogElement) {
  this.open = false
}
