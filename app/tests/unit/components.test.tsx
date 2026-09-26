import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { ConfirmDialog, NoticeProvider, ProgressBar, StatusBadge, useNotify } from '../../src/renderer/src/components'
import { Buckets } from '../../src/renderer/src/pages/Buckets'
import { Setup } from '../../src/renderer/src/pages/Setup'
import { installBridge } from './setup'

describe('accessible components', () => {
  it('status badge conveys state with text, not only colour', () => {
    render(<StatusBadge health="degraded" label="Repairing" />)
    expect(screen.getByText('Repairing')).toBeVisible()
  })

  it('progress bar exposes value to assistive tech', () => {
    render(<ProgressBar value={25} max={100} label="Uploading scan.dcm" />)
    const bar = screen.getByRole('progressbar', { name: 'Uploading scan.dcm' })
    expect(bar).toHaveAttribute('aria-valuenow', '25')
  })

  it('errors are announced through an alert region', async () => {
    function Trigger(): JSX.Element {
      const notify = useNotify()
      return <button onClick={() => notify('Upload failed', 'error')}>go</button>
    }
    render(
      <NoticeProvider>
        <Trigger />
      </NoticeProvider>
    )
    await userEvent.click(screen.getByRole('button', { name: 'go' }))
    expect(screen.getByRole('alert')).toHaveTextContent('Upload failed')
  })

  it('confirm dialog is labelled and calls back', async () => {
    const onConfirm = vi.fn()
    render(<ConfirmDialog open title="Delete scan?" body="Gone everywhere." confirmLabel="Delete" danger onConfirm={onConfirm} onCancel={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: 'Delete', hidden: true }))
    expect(onConfirm).toHaveBeenCalled()
  })
})

describe('Buckets page', () => {
  it('explains invalid policies and blocks submission', async () => {
    installBridge()
    render(
      <NoticeProvider>
        <Buckets />
      </NoticeProvider>
    )
    const w = screen.getByLabelText('Write quorum (W)')
    await userEvent.clear(w)
    await userEvent.type(w, '1')
    const r = screen.getByLabelText('Read quorum (R)')
    await userEvent.clear(r)
    await userEvent.type(r, '1')
    expect(await screen.findByText(/R \+ W must be greater than N/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Create bucket' })).toBeDisabled()
  })
})

describe('Setup wizard', () => {
  it('requires an invite code when joining and reports server errors', async () => {
    const setup = vi.fn().mockResolvedValue({ ok: false, status: 500, data: null, error: 'could not join via 10.0.0.2:19000' })
    installBridge({ setup })
    render(
      <NoticeProvider>
        <Setup
          initial={{ setupComplete: false, zone: 'laptop-a', storageDir: '', demoMode: true, advertise: '', nodes: [], version: '1.0.0' }}
          onDone={() => undefined}
        />
      </NoticeProvider>
    )
    await userEvent.click(screen.getByLabelText(/Join an existing cluster/))
    const code = screen.getByLabelText('Invite code')
    expect(code).toBeRequired()
    await userEvent.type(code, 'vault1.abc')
    await userEvent.click(screen.getByRole('button', { name: 'Join cluster' }))
    await waitFor(() => expect(setup).toHaveBeenCalledWith(expect.objectContaining({ mode: 'join', inviteCode: 'vault1.abc' })))
    expect(await screen.findByRole('alert')).toHaveTextContent('could not join')
  })
})
