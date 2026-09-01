// @vitest-environment jsdom
import { App, ConfigProvider, Grid } from 'antd'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '../i18n'
import { transferEvent } from '../hooks/useRuntimeEvents'
import { APIError, api } from '../services/api'
import RoutesPage, { compareAndClearOneTimeCode, initialTransferQueueState, pruneTerminalSummaries, transferQueueReducer, type QueuedFile } from './RoutesPage'

const { config } = vi.hoisted(() => ({ config: { auth_mode: 'demo' as const, unsafe_ssh: false, version: 'test', transfers: { max_file_bytes: 4, max_share_bytes: 8, max_job_bytes: 8, max_owner_bytes: 16, max_files_per_share: 2, max_owner_files: 4096, max_retained_shares_per_owner: 128, max_retained_jobs_per_owner: 128, workers: 4 as const, max_jobs_per_owner: 2, expiry_seconds: 86400, retention_seconds: 86400, upload_timeout_seconds: 1800 } } }))
vi.mock('../app/auth', () => ({ useAuth: () => ({ config, user: { id: 'user-1' }, logout: vi.fn() }) }))

const timestamp = '2026-09-01T12:00:00Z'
const server = { id: 'server-1', name: 'Studio server', key_mode: 'saved' as const, region: 'auto', exit_node_enabled: false, allowlist_enabled: false, desired_running: true, runtime_state: 'running' as const, mapping_count: 0, allowed_key_count: 0, created_at: timestamp, updated_at: timestamp }
const client = { id: 'client-1', name: 'Laptop client', saved_key: true, token_hint: 'tailcat=…', runtime_state: 'ready' as const, created_at: timestamp, updated_at: timestamp }
const readyJob = { id: '2f4c51fa-8d36-4c39-b9e4-4af06fe6189c', client_id: client.id, remote_share_id: 'remote-1', status: 'ready' as const, total_bytes: 4, received_bytes: 0, expires_at: timestamp, created_at: timestamp, updated_at: timestamp }
const failedJob = { ...readyJob, id: '3f4c51fa-8d36-4c39-b9e4-4af06fe6189c', status: 'failed' as const, received_bytes: 2, error_code: 'transfer_integrity_mismatch' as const }
const completedJob = { ...readyJob, id: '4f4c51fa-8d36-4c39-b9e4-4af06fe6189c', status: 'completed' as const, received_bytes: 4, finished_at: timestamp }
const runningJob = { ...readyJob, id: '5f4c51fa-8d36-4c39-b9e4-4af06fe6189c', status: 'running' as const, received_bytes: 1 }
const stagingShare = { id: '6f4c51fa-8d36-4c39-b9e4-4af06fe6189c', server_id: server.id, status: 'staging' as const, total_bytes: 0, file_count: 0, expires_at: timestamp, created_at: timestamp, updated_at: timestamp }
const readyShare = { ...stagingShare, status: 'ready' as const, total_bytes: 3, file_count: 1, ready_at: timestamp }
const route = { id: 'route-1', client_id: client.id, name: 'Existing published route', slug: 'existing', remote_port: 8080, base_path: '/', access: 'private' as const, enabled: true, url: '/r/existing', created_at: timestamp, updated_at: timestamp, allowed_methods: ['GET'] }

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => { resolve = resolvePromise; reject = rejectPromise })
  return { promise, resolve, reject }
}

function renderPage(entry = '/routes?tab=transfers') {
  return render(<ConfigProvider><App><MemoryRouter initialEntries={[entry]}><RoutesPage /></MemoryRouter></App></ConfigProvider>)
}

describe('RoutesPage transfers', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    config.transfers.max_file_bytes = 4
    config.transfers.max_share_bytes = 8
    config.transfers.max_files_per_share = 2
    vi.spyOn(api, 'routes').mockResolvedValue([])
    vi.spyOn(api, 'clients').mockResolvedValue([client])
    vi.spyOn(api, 'servers').mockResolvedValue([server])
    vi.spyOn(api, 'transferShares').mockResolvedValue([])
    vi.spyOn(api, 'transferJobs').mockResolvedValue([readyJob, runningJob, failedJob, completedJob])
    vi.spyOn(api, 'transferJobItems').mockResolvedValue([{ id: 'item-1', job_id: completedJob.id, virtual_path: '资料/very-long-file-name.txt', size: 4, status: 'completed', received_bytes: 4, completed_blocks: 1, mtime: timestamp, created_at: timestamp, updated_at: timestamp }])
  })
  afterEach(() => vi.useRealTimers())

  it('deep-links without adding navigation and exposes sender/receiver limits, recovery actions and no native dialogs', async () => {
    const alertSpy = vi.spyOn(window, 'alert').mockImplementation(() => undefined)
    const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const promptSpy = vi.spyOn(window, 'prompt').mockReturnValue('')
    renderPage()
    expect(await screen.findByRole('tab', { name: 'Transfers', selected: true })).not.toBeNull()
    expect(screen.getAllByText('Configured limits').length).toBeGreaterThan(0)
    expect(screen.getByText('Files per workspace')).not.toBeNull()
    expect(screen.getByText('Retained shares')).not.toBeNull()
    expect(screen.getByText('Retained jobs')).not.toBeNull()
    expect(screen.getByText('4,096')).not.toBeNull()
    expect(screen.getAllByText('Send files').length).toBeGreaterThan(0)
    expect(screen.getByRole('combobox', { name: 'Tailcat client' })).not.toBeNull()
    expect(screen.getByLabelText('One-time share code').getAttribute('type')).toBe('password')
    expect(screen.getByText('Start transfer')).not.toBeNull()
    expect(screen.getByText('Retry transfer')).not.toBeNull()
    expect(screen.getByText('Integrity verification failed. Retry to receive verified blocks again.')).not.toBeNull()
    expect(screen.getAllByText('File details').length).toBeGreaterThan(0)
    expect(alertSpy).not.toHaveBeenCalled()
    expect(confirmSpy).not.toHaveBeenCalled()
    expect(promptSpy).not.toHaveBeenCalled()
  })

  it('rejects files over the configured limit before creating a share', async () => {
    const create = vi.spyOn(api, 'createTransferShare')
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    const send = screen.getAllByText('Send files').find((node) => node.closest('button'))?.closest('button')
    if (!send) throw new Error('Send button missing')
    await user.click(send)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files: [new File(['12345'], 'large.txt')] } })
    expect(await screen.findByText('large.txt exceeds the configured per-file limit.')).not.toBeNull()
    expect(create).not.toHaveBeenCalled()
  })

  it.each([
    ['file-count', 2, [new File(['a'], 'a.txt'), new File(['b'], 'b.txt'), new File(['c'], 'c.txt')], 'The selected files exceed the configured file-count limit.'],
    ['share-size', 3, [new File(['1234'], 'a.txt'), new File(['1234'], 'b.txt'), new File(['x'], 'c.txt')], 'The selected files exceed the configured per-share limit.'],
  ])('rejects the configured %s limit before share creation', async (_name, maxFiles, files, error) => {
    config.transfers.max_files_per_share = maxFiles
    const create = vi.spyOn(api, 'createTransferShare')
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    const send = screen.getAllByText('Send files').find((node) => node.closest('button'))?.closest('button')
    if (!send) throw new Error('Send button missing')
    await user.click(send)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files } })
    expect(await screen.findByText(error)).not.toBeNull()
    expect(create).not.toHaveBeenCalled()
  })

  it('creates a receive job without persisting its capability and starts it explicitly', async () => {
    const create = vi.spyOn(api, 'createTransferJob').mockResolvedValue(readyJob)
    const start = vi.spyOn(api, 'startTransferJob').mockResolvedValue({ ...readyJob, status: 'running' })
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    const capability = screen.getByLabelText('One-time share code')
    await user.type(capability, 'tcs1.secret')
    await user.click(screen.getByText('Create receive job'))
    await waitFor(() => expect(create).toHaveBeenCalledWith({ client_id: 'client-1', capability: 'tcs1.secret' }))
    expect((capability as HTMLInputElement).value).toBe('')
    await user.click(screen.getAllByText('Start transfer')[0]!)
    await waitFor(() => expect(start).toHaveBeenCalledWith(readyJob.id))
  })

  it('keeps stable queue IDs and retries only state transitions through the reducer', () => {
    const first: QueuedFile = { uid: 'upload-1', file: new File(['one'], 'one.txt'), virtualPath: 'one.txt', status: 'queued' }
    const second: QueuedFile = { uid: 'upload-2', file: new File(['two'], 'two.txt'), virtualPath: 'nested/two.txt', status: 'queued' }
    let state = transferQueueReducer(initialTransferQueueState, { type: 'add', files: [first, second, first] })
    expect(state.items.map((item) => item.uid)).toEqual(['upload-1', 'upload-2'])
    state = transferQueueReducer(state, { type: 'begin', operationID: 1, uids: ['upload-1', 'upload-2'] })
    expect(transferQueueReducer(state, { type: 'remove', uid: 'upload-1' })).toBe(state)
    expect(transferQueueReducer(state, { type: 'add', files: [{ ...second, uid: 'upload-3' }] })).toBe(state)
    state = transferQueueReducer(state, { type: 'status', operationID: 1, uid: 'upload-2', status: 'failed', error: 'offline' })
    expect(state.items[1]).toMatchObject({ uid: 'upload-2', status: 'failed', error: 'offline' })
    state = transferQueueReducer(state, { type: 'finish', operationID: 1, clearSucceeded: false })
    state = transferQueueReducer(state, { type: 'remove', uid: 'upload-1' })
    expect(state.items.map((item) => item.uid)).toEqual(['upload-2'])
  })

  it('applies only monotonic transfer progress immediately and coalesces transfer-only refreshes', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const jobs = vi.mocked(api.transferJobs)
    const routes = vi.mocked(api.routes)
    const event = (sequence: number, received: number) => new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: readyJob.id, operation_id: readyJob.id, phase: 'running', sequence, at: timestamp, payload: { job_id: readyJob.id, status: 'running', received_bytes: received, total_bytes: 4, completed_files: 0, total_files: 1 } } })
    vi.useFakeTimers()
    act(() => { window.dispatchEvent(event(4, 2)); window.dispatchEvent(event(3, 1)); window.dispatchEvent(event(5, 3)) })
    expect(document.body.textContent).toContain('3 B / 4 B')
    expect(jobs).toHaveBeenCalledTimes(1)
    act(() => vi.advanceTimersByTime(99))
    expect(jobs).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(1); await Promise.resolve(); await Promise.resolve() })
    expect(jobs).toHaveBeenCalledTimes(2)
    expect(routes).toHaveBeenCalledTimes(1)
  })

  it('polls durable transfer history so a missed terminal event is reconciled', async () => {
    const intervals = vi.spyOn(window, 'setInterval')
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const shares = vi.mocked(api.transferShares)
    const jobs = vi.mocked(api.transferJobs)
    expect(shares).toHaveBeenCalledTimes(1)
    expect(jobs).toHaveBeenCalledTimes(1)
    const reconcile = await waitFor(() => {
      const callback = intervals.mock.calls.find((call) => call[1] === 5000)?.[0]
      if (typeof callback !== 'function') throw new Error('transfer reconcile interval was not installed')
      return callback
    })
    await act(async () => { reconcile(); await Promise.resolve(); await Promise.resolve() })
    await waitFor(() => expect(shares).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(jobs).toHaveBeenCalledTimes(2))
  })

  it('shows completed item downloads only from the authenticated same-origin endpoint', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    await waitFor(() => expect(document.body.textContent).toContain('1 / 1 files'))
    const details = await screen.findAllByText('File details')
    const user = userEvent.setup()
    await user.click(details.at(-1)!)
    const download = await screen.findByText('Download')
    const link = download.closest('a')
    expect(link?.getAttribute('href')).toBe(`/api/v1/transfers/jobs/${completedJob.id}/items/item-1/download`)
    expect(link?.hasAttribute('download')).toBe(true)
    expect(link?.getAttribute('href')).not.toContain('capability')
  })

  it('uploads sequentially, finalizes, and keeps the one-time code until explicit confirmation', async () => {
    const capability = 'tcs1.one-time-capability'
    const create = vi.spyOn(api, 'createTransferShare').mockResolvedValue({ share: stagingShare, capability })
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockResolvedValue({ id: 'file-1', virtual_path: 'notes.txt', size: 3, mtime: timestamp, created_at: timestamp })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    const user = userEvent.setup()
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) }
    Object.defineProperty(navigator, 'clipboard', { configurable: true, value: clipboard })
    const storage = vi.spyOn(Storage.prototype, 'setItem')
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const send = screen.getAllByText('Send files').find((node) => node.closest('button'))?.closest('button')
    if (!send) throw new Error('Send button missing')
    await user.click(send)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    const file = new File(['abc'], 'notes.txt')
    const secondFile = new File(['de'], 'second.txt')
    fireEvent.change(fileInput, { target: { files: [file, secondFile] } })
    await user.click(await screen.findByText('Upload and finalize'))
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(create).toHaveBeenCalledWith({ server_id: server.id })
    expect(upload).toHaveBeenNthCalledWith(1, stagingShare.id, file, 'notes.txt', expect.any(AbortSignal))
    expect(upload).toHaveBeenNthCalledWith(2, stagingShare.id, secondFile, 'second.txt', expect.any(AbortSignal))
    expect(upload.mock.calls[0]?.[3]).toBe(upload.mock.calls[1]?.[3])
    expect(document.body.textContent).toContain(capability)
    expect(screen.getByText('I saved the code')).not.toBeNull()
    const copy = screen.getByLabelText('Copy')
    fireEvent.click(copy)
    await waitFor(() => expect(clipboard.writeText).toHaveBeenCalledWith(capability))
    expect(storage.mock.calls.some((call) => call.includes(capability))).toBe(false)
    await user.click(screen.getByText('I saved the code'))
    expect(document.body.textContent).not.toContain(capability)
  })

  it('resumes a staging sender after an upload failure without creating a second share', async () => {
    const create = vi.spyOn(api, 'createTransferShare').mockResolvedValue({ share: stagingShare, capability: 'tcs1.retry-code' })
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockRejectedValueOnce(new APIError(503, 'TRANSFERS_UNAVAILABLE', 'offline')).mockResolvedValue({ id: 'file-1', virtual_path: 'retry.txt', size: 3, mtime: timestamp, created_at: timestamp })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    const send = screen.getAllByText('Send files').find((node) => node.closest('button'))?.closest('button')
    if (!send) throw new Error('Send button missing')
    await user.click(send)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files: [new File(['abc'], 'retry.txt')] } })
    await user.click(await screen.findByText('Upload and finalize'))
    expect((await screen.findAllByText('Secure transfers are temporarily unavailable.')).length).toBeGreaterThan(0)
    const retryButton = screen.getAllByText('Retry uploads').find((node) => node.closest('button'))?.closest('button')
    if (!retryButton) throw new Error('Retry button missing')
    await user.click(retryButton)
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(create).toHaveBeenCalledTimes(1)
    expect(upload).toHaveBeenCalledTimes(2)
  })

  it('aborts the active upload, skips remaining files, and retries the same staging share', async () => {
    config.transfers.max_files_per_share = 4
    const create = vi.spyOn(api, 'createTransferShare').mockResolvedValue({ share: stagingShare, capability: 'tcs1.cancel-code' })
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockImplementation((_shareID, file, virtualPath, signal) => {
      if (upload.mock.calls.length === 1) {
        return new Promise((_, reject) => {
          signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
        })
      }
      return Promise.resolve({ id: `file-${virtualPath}`, virtual_path: virtualPath, size: file.size, mtime: timestamp, created_at: timestamp })
    })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    renderPage()
    const user = userEvent.setup()
    await user.click((await screen.findAllByText('Send files')).find((node) => node.closest('button'))!)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files: [new File(['a'], 'first.txt'), new File(['b'], 'second.txt')] } })
    await user.click(await screen.findByText('Upload and finalize'))
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1))
    const cancelButtons = await screen.findAllByRole('button', { name: 'Cancel uploads' })
    await user.click(cancelButtons.at(-1)!)
    expect(upload.mock.calls[0]?.[3]?.aborted).toBe(true)
    const retryButton = await waitFor(() => {
      const button = screen.getAllByText('Retry uploads').find((node) => node.closest('button'))?.closest('button')
      if (!button) throw new Error('Retry uploads button missing')
      return button
    })
    expect(upload).toHaveBeenCalledTimes(1)
    expect(finalize).not.toHaveBeenCalled()
    await user.click(retryButton)
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(create).toHaveBeenCalledTimes(1)
    expect(upload.mock.calls.map((call) => (call[1] as File).name)).toEqual(['first.txt', 'first.txt', 'second.txt'])
  })

  it('aborts the active upload when the route unmounts', async () => {
    let uploadSignal: AbortSignal | undefined
    vi.spyOn(api, 'createTransferShare').mockResolvedValue({ share: stagingShare, capability: 'tcs1.unmount-code' })
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockImplementation((_shareID, _file, _virtualPath, signal) => {
      uploadSignal = signal
      return new Promise((_, reject) => {
        signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
      })
    })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    const view = renderPage()
    const user = userEvent.setup()
    await user.click((await screen.findAllByText('Send files')).find((node) => node.closest('button'))!)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files: [new File(['a'], 'first.txt')] } })
    await user.click(await screen.findByText('Upload and finalize'))
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1))
    view.unmount()
    expect(uploadSignal?.aborted).toBe(true)
    expect(finalize).not.toHaveBeenCalled()
  })

  it('confirms rotation/deletion and exposes start, cancel and retry job actions', async () => {
    vi.mocked(api.transferShares).mockResolvedValue([readyShare])
    const rotate = vi.spyOn(api, 'rotateTransferShare').mockResolvedValue({ capability: 'tcs1.rotated-code' })
    const remove = vi.spyOn(api, 'deleteTransferShare').mockResolvedValue(undefined)
    const deleteJob = vi.spyOn(api, 'deleteTransferJob').mockResolvedValue(undefined)
    const cancel = vi.spyOn(api, 'cancelTransferJob').mockResolvedValue(undefined)
    const retry = vi.spyOn(api, 'retryTransferJob').mockResolvedValue({ ...failedJob, status: 'running' })
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    await user.click(screen.getByText('Rotate code'))
    const rotatePopup = Array.from(document.querySelectorAll('.ant-popconfirm')).at(-1)
    if (!(rotatePopup instanceof HTMLElement)) throw new Error('Rotation confirmation missing')
    await user.click(within(rotatePopup).getByText('Rotate code'))
    await waitFor(() => expect(rotate).toHaveBeenCalledWith(readyShare.id))
    expect(document.body.textContent).toContain('tcs1.rotated-code')
    await user.click(screen.getByText('Cancel transfer'))
    await user.click(screen.getByText('Retry transfer'))
    await waitFor(() => expect(cancel).toHaveBeenCalledWith(runningJob.id))
    await waitFor(() => expect(retry).toHaveBeenCalledWith(failedJob.id))
    const shareDelete = document.querySelector('.transfer-list .ant-list-item button.ant-btn-dangerous')
    if (!(shareDelete instanceof HTMLElement)) throw new Error('Share delete button missing')
    fireEvent.click(shareDelete)
    const deletePopup = await waitFor(() => Array.from(document.querySelectorAll('.ant-popconfirm')).find((node) => node.textContent?.includes('Delete this share?')))
    if (!(deletePopup instanceof HTMLElement)) throw new Error('Delete confirmation missing')
    await user.click(within(deletePopup).getByText('Delete'))
    await waitFor(() => expect(remove).toHaveBeenCalledWith(readyShare.id))
    const incoming = document.querySelectorAll('.transfer-history')[1]
    const jobDeleteButton = incoming?.querySelector('button.ant-btn-dangerous')
    if (!(jobDeleteButton instanceof HTMLElement)) throw new Error('Job delete button missing')
    fireEvent.click(jobDeleteButton)
    const jobDeletePopup = await waitFor(() => Array.from(document.querySelectorAll('.ant-popconfirm')).find((node) => node.textContent?.includes('Delete this receive job?')))
    if (!(jobDeletePopup instanceof HTMLElement)) throw new Error('Job delete confirmation missing')
    await user.click(within(jobDeletePopup).getByText('Delete'))
    await waitFor(() => expect(deleteJob).toHaveBeenCalledWith(readyJob.id))
  })

  it('gives distinct recovery guidance for expired, remote, limit, integrity and canceled jobs', async () => {
    vi.mocked(api.transferJobs).mockResolvedValue([
      { ...failedJob, id: '7f4c51fa-8d36-4c39-b9e4-4af06fe6189c', status: 'expired', error_code: 'transfer_expired' },
      { ...failedJob, id: '8f4c51fa-8d36-4c39-b9e4-4af06fe6189c', error_code: 'transfer_remote_unavailable' },
      { ...failedJob, id: '9f4c51fa-8d36-4c39-b9e4-4af06fe6189c', error_code: 'transfer_limit_exceeded' },
      failedJob,
      { ...failedJob, id: 'af4c51fa-8d36-4c39-b9e4-4af06fe6189c', status: 'canceled', error_code: 'transfer_canceled' },
    ])
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    expect(document.body.textContent).toContain('The share expired. Ask the sender to create a new share.')
    expect(document.body.textContent).toContain('The remote peer is unavailable. Check its Tailcat runtime, then retry.')
    expect(document.body.textContent).toContain('A configured transfer limit was reached. Reduce the selection or remove older transfers.')
    expect(document.body.textContent).toContain('Integrity verification failed. Retry to receive verified blocks again.')
    expect(document.body.textContent).toContain('The transfer was canceled. Retry to resume verified blocks.')
  })

  it('uses comparison tables at desktop width and mobile cards without horizontal tables', async () => {
    vi.spyOn(Grid, 'useBreakpoint').mockReturnValue({ md: true, lg: true })
    vi.mocked(api.transferShares).mockResolvedValue([readyShare])
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    expect(document.querySelectorAll('.transfer-table').length).toBe(2)
    expect(document.querySelector('.transfer-mobile-card')).toBeNull()
  })

  it('freezes the sender queue, aborts on drawer close, and retries the preserved files', async () => {
    config.transfers.max_files_per_share = 4
    const create = vi.spyOn(api, 'createTransferShare').mockResolvedValue({ share: stagingShare, capability: 'tcs1.locked-code' })
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockImplementation((_shareID, file, virtualPath, signal) => {
      if (upload.mock.calls.length === 1) {
        return new Promise((_, reject) => {
          signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
        })
      }
      return Promise.resolve({ id: `file-${virtualPath}`, virtual_path: virtualPath, size: file.size, mtime: timestamp, created_at: timestamp })
    })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    await user.click(screen.getAllByText('Send files').find((node) => node.closest('button'))!)
    const fileInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    const first = new File(['1'], 'first.txt')
    const removed = new File(['2'], 'removed.txt')
    fireEvent.change(fileInput, { target: { files: [first, removed] } })
    await user.click(await screen.findByLabelText('Delete removed.txt'))
    await user.click(await screen.findByText('Upload and finalize'))
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1))
    await waitFor(() => expect((document.querySelector('input[type="file"]') as HTMLInputElement | null)?.disabled).toBe(true))
    expect((screen.getByRole('combobox', { name: 'Tailcat server' }) as HTMLInputElement).disabled).toBe(true)
    expect(screen.getByRole('button', { name: 'Close' })).not.toBeNull()
    expect((screen.getByLabelText('Delete first.txt') as HTMLButtonElement).disabled).toBe(true)
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.querySelector('.ant-drawer-open')).toBeNull())
    expect(upload.mock.calls[0]?.[3]?.aborted).toBe(true)
    await waitFor(() => expect(screen.queryByText('Upload in progress')).toBeNull())
    expect(finalize).not.toHaveBeenCalled()
    expect(document.body.textContent).toContain('tcs1.locked-code')
    const reopen = screen.getAllByText('Send files').find((node) => node.closest('button'))?.closest('button')
    if (!reopen) throw new Error('Send button missing')
    await user.click(reopen)
    expect(await screen.findByLabelText('Delete first.txt')).not.toBeNull()
    const late = new File(['3'], 'late.txt')
    const reopenedInput = await waitFor(() => document.querySelector('input[type="file"]'))
    if (!(reopenedInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(reopenedInput, { target: { files: [late] } })
    const retryButton = screen.getAllByText('Retry uploads').find((node) => node.closest('button'))?.closest('button')
    if (!retryButton) throw new Error('Retry uploads button missing')
    await user.click(retryButton)
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(create).toHaveBeenCalledTimes(1)
    expect(upload.mock.calls.map((call) => (call[1] as File).name)).toEqual(['first.txt', 'first.txt', 'late.txt'])
    expect(new Set(upload.mock.calls.slice(1).map((call) => call[2])).size).toBe(upload.mock.calls.length - 1)
  })

  it('reconciles an API-history staging share before resume and applies remaining limits and paths', async () => {
    const existing = { id: 'existing-1', virtual_path: 'existing.txt', size: 3, mtime: timestamp, created_at: timestamp }
    vi.mocked(api.transferShares).mockResolvedValue([{ ...stagingShare, total_bytes: 3, file_count: 1 }])
    const history = vi.spyOn(api, 'transferShareFiles').mockResolvedValue([existing])
    const upload = vi.spyOn(api, 'uploadTransferShareFile').mockResolvedValue({ id: 'new-1', virtual_path: 'new.txt', size: 4, mtime: timestamp, created_at: timestamp })
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('Try again'))
    await waitFor(() => expect(history).toHaveBeenCalledWith(stagingShare.id))
    expect(document.body.textContent).toContain('3 B / 3 B · 1 / 1 files')
    expect(screen.getAllByText('Finalize').length).toBeGreaterThan(0)
    const fileInput = document.querySelector('input[type="file"]')
    if (!(fileInput instanceof HTMLInputElement)) throw new Error('Upload input missing')
    fireEvent.change(fileInput, { target: { files: [new File(['x'], 'existing.txt')] } })
    expect(await screen.findByText('A file with this virtual path is already staged or queued.')).not.toBeNull()
    const refreshedInput = document.querySelector('input[type="file"]')
    if (!(refreshedInput instanceof HTMLInputElement)) throw new Error('Refreshed upload input missing')
    fireEvent.change(refreshedInput, { target: { files: [new File(['1234'], 'new.txt')] } })
    expect(await screen.findByText('new.txt')).not.toBeNull()
    await user.click(await screen.findByText('Upload and finalize'))
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(upload).toHaveBeenCalledTimes(1)
    expect(upload.mock.calls[0]?.[2]).toBe('new.txt')
  })

  it('keeps staging history load errors visible and retryable', async () => {
    vi.mocked(api.transferShares).mockResolvedValue([{ ...stagingShare, total_bytes: 3, file_count: 1 }])
    const history = vi.spyOn(api, 'transferShareFiles').mockRejectedValueOnce(new APIError(503, 'TRANSFERS_UNAVAILABLE', 'offline')).mockResolvedValue([{ id: 'existing-1', virtual_path: 'existing.txt', size: 3, mtime: timestamp, created_at: timestamp }])
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('Try again'))
    expect(await screen.findByText('Could not load the staged file history. Try again.')).not.toBeNull()
    await user.click(screen.getByText('Retry staged history'))
    await waitFor(() => expect(history).toHaveBeenCalledTimes(2))
    expect(document.body.textContent).toContain('3 B / 3 B · 1 / 1 files')
  })

  it('ignores stale A/B resume results and commits only the newest staging history', async () => {
    const shareB = { ...stagingShare, id: '7f4c51fa-8d36-4c39-b9e4-4af06fe6189c' }
    vi.mocked(api.transferShares).mockResolvedValue([stagingShare, shareB])
    const historyA = deferred<Array<{ id: string; virtual_path: string; size: number; mtime: string; created_at: string }>>()
    const historyB = deferred<Array<{ id: string; virtual_path: string; size: number; mtime: string; created_at: string }>>()
    vi.spyOn(api, 'transferShareFiles').mockImplementation((id) => id === stagingShare.id ? historyA.promise : historyB.promise)
    renderPage()
    const user = userEvent.setup()
    const resumeButtons = await screen.findAllByText('Try again')
    await user.click(resumeButtons[0]!)
    await user.click(resumeButtons[1]!)
    historyB.resolve([{ id: 'b', virtual_path: 'b.txt', size: 2, mtime: timestamp, created_at: timestamp }])
    expect(await screen.findByText(/2 B \/ 2 B · 1 \/ 1 files/)).not.toBeNull()
    historyA.resolve([{ id: 'a', virtual_path: 'a.txt', size: 1, mtime: timestamp, created_at: timestamp }])
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(document.querySelector('.ant-drawer')?.textContent).toContain('2 B / 2 B · 1 / 1 files')
    expect(document.querySelector('.ant-drawer')?.textContent).not.toContain('1 B / 1 B')
  })

  it('invalidates a pending resume when its share is deleted or the page unmounts', async () => {
    vi.mocked(api.transferShares).mockResolvedValue([stagingShare])
    const history = deferred<Array<{ id: string; virtual_path: string; size: number; mtime: string; created_at: string }>>()
    vi.spyOn(api, 'transferShareFiles').mockReturnValue(history.promise)
    vi.spyOn(api, 'deleteTransferShare').mockResolvedValue(undefined)
    const view = renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('Try again'))
    const deleteButton = document.querySelector('.transfer-history button.ant-btn-dangerous')
    if (!(deleteButton instanceof HTMLButtonElement)) throw new Error('Delete button missing')
    fireEvent.click(deleteButton)
    const popup = await waitFor(() => Array.from(document.querySelectorAll('.ant-popconfirm')).find((node) => node.textContent?.includes('Delete this share?')))
    await user.click(within(popup as HTMLElement).getByText('Delete'))
    history.resolve([{ id: 'late', virtual_path: 'late.txt', size: 1, mtime: timestamp, created_at: timestamp }])
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(document.querySelector('.ant-drawer-open')).toBeNull()
    view.unmount()
    vi.mocked(api.transferShares).mockResolvedValue([stagingShare])
    const afterUnmount = deferred<Array<{ id: string; virtual_path: string; size: number; mtime: string; created_at: string }>>()
    vi.mocked(api.transferShareFiles).mockReturnValue(afterUnmount.promise)
    const secondView = renderPage()
    await user.click(await screen.findByText('Try again'))
    secondView.unmount()
    afterUnmount.resolve([])
    await Promise.resolve()
  })

  it('finalizes a reconciled staging share with existing files and no new queue', async () => {
    vi.mocked(api.transferShares).mockResolvedValue([{ ...stagingShare, total_bytes: 3, file_count: 1 }])
    vi.spyOn(api, 'transferShareFiles').mockResolvedValue([{ id: 'existing-1', virtual_path: 'existing.txt', size: 3, mtime: timestamp, created_at: timestamp }])
    const upload = vi.spyOn(api, 'uploadTransferShareFile')
    const finalize = vi.spyOn(api, 'finalizeTransferShare').mockResolvedValue(readyShare)
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('Try again'))
    const finalizeButton = (await screen.findAllByText('Finalize')).find((node) => node.closest('.ant-drawer'))
    if (!finalizeButton) throw new Error('Drawer finalize action missing')
    await user.click(finalizeButton)
    await waitFor(() => expect(finalize).toHaveBeenCalledWith(stagingShare.id))
    expect(upload).not.toHaveBeenCalled()
  })

  it('coalesces many live job events without requesting unselected item lists', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const items = vi.mocked(api.transferJobItems)
    await waitFor(() => expect(vi.mocked(api.transferJobs)).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(items).toHaveBeenCalledWith(completedJob.id))
    items.mockClear()
    vi.useFakeTimers()
    const ids = [readyJob.id, runningJob.id, failedJob.id, completedJob.id]
    act(() => ids.forEach((id, index) => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: id, operation_id: id, phase: 'running', sequence: index + 1, at: timestamp, payload: { job_id: id, status: 'running', received_bytes: index, total_bytes: 4, completed_files: 0, total_files: 1 } } }))))
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(vi.mocked(api.transferJobs)).toHaveBeenCalledTimes(2)
    expect(items).toHaveBeenCalledTimes(0)
  })

  it('loads and caches an initial completed job summary once, then prunes deleted IDs', async () => {
    renderPage()
    await waitFor(() => expect(api.transferJobItems).toHaveBeenCalledWith(completedJob.id))
    await waitFor(() => expect(document.body.textContent).toContain('1 / 1 files'))
    vi.mocked(api.transferJobItems).mockClear()
    vi.useFakeTimers()
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: runningJob.id, operation_id: runningJob.id, phase: 'running', sequence: 1, at: timestamp, payload: { job_id: runningJob.id, status: 'running', received_bytes: 2, total_bytes: 4 } } })))
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(api.transferJobItems).not.toHaveBeenCalled()
    expect(pruneTerminalSummaries({ [completedJob.id]: { completed: 1, total: 1 }, gone: { completed: 2, total: 2 } }, [completedJob])).toEqual({ [completedJob.id]: { completed: 1, total: 1 } })
  })

  it('preserves terminal event file totals after durable live-patch cleanup', async () => {
    vi.mocked(api.transferJobs).mockResolvedValueOnce([runningJob]).mockResolvedValue([{ ...runningJob, status: 'completed', received_bytes: 4, finished_at: timestamp }])
    renderPage()
    await screen.findByText('Running')
    vi.useFakeTimers()
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: runningJob.id, operation_id: runningJob.id, phase: 'ready', sequence: 1, at: timestamp, payload: { job_id: runningJob.id, status: 'completed', received_bytes: 4, total_bytes: 4, completed_files: 1, total_files: 2 } } })))
    expect(document.body.textContent).toContain('1 / 2 files')
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve(); await Promise.resolve() })
    expect(document.body.textContent).toContain('1 / 2 files')
  })

  it('refreshes only the selected Drawer item list once for an event burst', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    const user = userEvent.setup()
    await user.click((await screen.findAllByText('File details'))[0]!)
    await waitFor(() => expect(api.transferJobItems).toHaveBeenCalledWith(readyJob.id))
    vi.mocked(api.transferJobItems).mockClear()
    vi.useFakeTimers()
    act(() => [1, 2, 3].forEach((sequence) => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: readyJob.id, operation_id: readyJob.id, phase: 'running', sequence, at: timestamp, payload: { job_id: readyJob.id, status: 'running', received_bytes: sequence, total_bytes: 4, completed_files: 0, total_files: 1 } } }))))
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(api.transferJobItems).toHaveBeenCalledTimes(1)
    expect(api.transferJobItems).toHaveBeenCalledWith(readyJob.id)
  })

  it('derives an open Drawer from current live state and unlocks downloads only after selected items refresh', async () => {
    const runningItem = { id: 'item-live', job_id: runningJob.id, virtual_path: 'live.txt', size: 4, status: 'running' as const, received_bytes: 1, completed_blocks: 0, mtime: timestamp, created_at: timestamp, updated_at: timestamp }
    const completedItem = { ...runningItem, status: 'completed' as const, received_bytes: 4, completed_blocks: 1, finished_at: timestamp }
    const refreshedItems = deferred<typeof completedItem[]>()
    vi.mocked(api.transferJobItems).mockImplementation(async (jobID) => jobID === runningJob.id && vi.mocked(api.transferJobItems).mock.calls.filter((call) => call[0] === jobID).length > 1 ? refreshedItems.promise : [runningItem])
    vi.mocked(api.transferJobs).mockResolvedValueOnce([readyJob, runningJob, failedJob, completedJob]).mockResolvedValue([{ ...runningJob, status: 'completed', received_bytes: 4, finished_at: timestamp }])
    renderPage()
    const user = userEvent.setup()
    const details = await screen.findAllByText('File details')
    await user.click(details[1]!)
    await waitFor(() => expect(document.querySelector('.ant-drawer')?.textContent).toContain('Running'))
    expect(screen.queryByText('Download')).toBeNull()
    vi.useFakeTimers()
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: runningJob.id, operation_id: runningJob.id, phase: 'ready', sequence: 1, at: timestamp, payload: { job_id: runningJob.id, status: 'completed', received_bytes: 4, total_bytes: 4, completed_files: 1, total_files: 1 } } })))
    expect(document.querySelector('.ant-drawer')?.textContent).toContain('Completed')
    expect(screen.queryByText('Download')).toBeNull()
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); refreshedItems.resolve([completedItem]); await Promise.resolve(); await Promise.resolve() })
    vi.useRealTimers()
    expect(await screen.findByText('Download')).not.toBeNull()
  })

  it('keeps selected item load failures explicit until retry succeeds', async () => {
    let readyAttempts = 0
    vi.mocked(api.transferJobItems).mockImplementation(async (jobID) => {
      if (jobID === readyJob.id && readyAttempts++ === 0) throw new Error('offline')
      return [{ id: 'item-1', job_id: jobID, virtual_path: 'retry.txt', size: 4, status: jobID === completedJob.id ? 'completed' : 'ready', received_bytes: jobID === completedJob.id ? 4 : 0, completed_blocks: jobID === completedJob.id ? 1 : 0, mtime: timestamp, created_at: timestamp, updated_at: timestamp }]
    })
    renderPage()
    const user = userEvent.setup()
    await user.click((await screen.findAllByText('File details'))[0]!)
    expect(await screen.findByText('Could not load the files for this job.')).not.toBeNull()
    expect(document.querySelector('.ant-drawer')?.textContent).toContain('File progress unavailable')
    expect(document.querySelector('.ant-drawer')?.textContent).not.toContain('0 / 0 files')
    await user.click(screen.getByText('Try again'))
    expect(await screen.findByText('retry.txt')).not.toBeNull()
  })

  it('refreshes the final selected lifecycle when SSE running arrives before the Retry response', async () => {
    const runningAfterRetry = { ...failedJob, status: 'running' as const, error_code: undefined }
    const terminalItem = { id: 'terminal-item', job_id: failedJob.id, virtual_path: 'terminal.txt', size: 4, status: 'completed' as const, received_bytes: 4, completed_blocks: 1, mtime: timestamp, created_at: timestamp, updated_at: timestamp, finished_at: timestamp }
    const currentItem = { ...terminalItem, id: 'sse-first-current-item', virtual_path: 'sse-first-current.txt', status: 'running' as const, received_bytes: 1, completed_blocks: 0, finished_at: undefined }
    const retryResponse = deferred<typeof runningAfterRetry>()
    const staleItems = deferred<typeof currentItem[]>()
    const currentItems = deferred<typeof currentItem[]>()
    let failedJobCalls = 0
    const items = vi.mocked(api.transferJobItems).mockImplementation(async (jobID) => {
      if (jobID !== failedJob.id) return []
      failedJobCalls += 1
      if (failedJobCalls === 1) return [terminalItem]
      if (failedJobCalls === 2) return staleItems.promise
      return currentItems.promise
    })
    vi.mocked(api.transferJobs).mockResolvedValue([failedJob])
    const retry = vi.spyOn(api, 'retryTransferJob').mockReturnValue(retryResponse.promise)
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('File details'))
    const drawer = document.querySelector('.ant-drawer-open') as HTMLElement
    expect(await within(drawer).findByText('terminal.txt')).not.toBeNull()
    expect(drawer.textContent).toContain('1 / 1 files')

    await user.click(within(drawer).getByText('Retry transfer'))
    expect(retry).toHaveBeenCalledTimes(1)
    vi.mocked(api.transferJobs).mockResolvedValue([runningAfterRetry])
    vi.useFakeTimers()
    act(() => {
      window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 1, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 2, total_bytes: 4 } } }))
      window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 2, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 3, total_bytes: 4 } } }))
    })
    expect(items).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(2)
    expect(drawer.textContent).toContain('Running')
    expect(within(drawer).getByText('Loading')).not.toBeNull()

    await act(async () => { retryResponse.resolve(runningAfterRetry); await Promise.resolve(); await Promise.resolve() })
    expect(within(drawer).queryByText('Loading')).toBeNull()
    expect(drawer.textContent).toContain('File progress unavailable')
    await act(async () => { staleItems.reject(new Error('stale offline')); await Promise.resolve(); await Promise.resolve() })
    expect(within(drawer).queryByText('Could not load the files for this job.')).toBeNull()

    act(() => vi.advanceTimersByTime(99))
    expect(items).toHaveBeenCalledTimes(2)
    await act(async () => { vi.advanceTimersByTime(1); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(3)
    expect(within(drawer).getByText('Loading')).not.toBeNull()

    await act(async () => { currentItems.resolve([currentItem]); await Promise.resolve(); await Promise.resolve() })
    expect(within(drawer).getByText('sse-first-current.txt')).not.toBeNull()
    expect(within(drawer).queryByText('terminal.txt')).toBeNull()
    expect(drawer.textContent).toContain('0 / 1 files')
    expect(items).toHaveBeenCalledTimes(3)
  })

  it('refreshes the selected lifecycle when the Retry response arrives before duplicate SSE running events', async () => {
    const runningAfterRetry = { ...failedJob, status: 'running' as const, error_code: undefined }
    const terminalItem = { id: 'terminal-item', job_id: failedJob.id, virtual_path: 'terminal.txt', size: 4, status: 'completed' as const, received_bytes: 4, completed_blocks: 1, mtime: timestamp, created_at: timestamp, updated_at: timestamp, finished_at: timestamp }
    const staleItem = { ...terminalItem, id: 'api-first-stale-item', virtual_path: 'api-first-stale.txt' }
    const currentItem = { ...terminalItem, id: 'api-first-current-item', virtual_path: 'api-first-current.txt', status: 'running' as const, received_bytes: 1, completed_blocks: 0, finished_at: undefined }
    const retryResponse = deferred<typeof runningAfterRetry>()
    const staleItems = deferred<typeof staleItem[]>()
    const currentItems = deferred<typeof currentItem[]>()
    let failedJobCalls = 0
    const items = vi.mocked(api.transferJobItems).mockImplementation(async (jobID) => {
      if (jobID !== failedJob.id) return []
      failedJobCalls += 1
      if (failedJobCalls === 1) return [terminalItem]
      if (failedJobCalls === 2) return staleItems.promise
      return currentItems.promise
    })
    vi.mocked(api.transferJobs).mockResolvedValue([failedJob])
    vi.spyOn(api, 'retryTransferJob').mockReturnValue(retryResponse.promise)
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('File details'))
    const drawer = document.querySelector('.ant-drawer-open') as HTMLElement
    expect(await within(drawer).findByText('terminal.txt')).not.toBeNull()

    await user.click(within(drawer).getByText('Retry transfer'))
    vi.mocked(api.transferJobs).mockResolvedValue([runningAfterRetry])
    vi.useFakeTimers()
    await act(async () => { retryResponse.resolve(runningAfterRetry); await Promise.resolve(); await Promise.resolve() })
    expect(drawer.textContent).toContain('Running')
    expect(drawer.textContent).toContain('File progress unavailable')
    expect(items).toHaveBeenCalledTimes(1)
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(2)
    expect(within(drawer).getByText('Loading')).not.toBeNull()

    act(() => {
      window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 1, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 2, total_bytes: 4 } } }))
      window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 2, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 3, total_bytes: 4 } } }))
    })
    act(() => vi.advanceTimersByTime(99))
    expect(items).toHaveBeenCalledTimes(2)
    await act(async () => { vi.advanceTimersByTime(1); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(3)
    expect(within(drawer).getByText('Loading')).not.toBeNull()

    await act(async () => { staleItems.resolve([staleItem]); await Promise.resolve(); await Promise.resolve() })
    expect(within(drawer).queryByText('api-first-stale.txt')).toBeNull()
    expect(within(drawer).getByText('Loading')).not.toBeNull()
    await act(async () => { currentItems.resolve([currentItem]); await Promise.resolve(); await Promise.resolve() })
    expect(within(drawer).getByText('api-first-current.txt')).not.toBeNull()
    expect(within(drawer).queryByText('terminal.txt')).toBeNull()
    expect(drawer.textContent).toContain('0 / 1 files')
    expect(items).toHaveBeenCalledTimes(3)
  })

  it.each(['Drawer close', 'job deletion', 'unmount'] as const)('cancels a pending lifecycle item refresh on %s', async (ending) => {
    const terminalItem = { id: 'terminal-item', job_id: failedJob.id, virtual_path: 'terminal.txt', size: 4, status: 'completed' as const, received_bytes: 4, completed_blocks: 1, mtime: timestamp, created_at: timestamp, updated_at: timestamp, finished_at: timestamp }
    const items = vi.mocked(api.transferJobItems).mockResolvedValue([terminalItem])
    vi.mocked(api.transferJobs).mockResolvedValue([failedJob])
    const view = renderPage()
    await userEvent.click(await screen.findByText('File details'))
    const drawer = document.querySelector('.ant-drawer-open') as HTMLElement
    expect(await within(drawer).findByText('terminal.txt')).not.toBeNull()

    vi.useFakeTimers()
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 1, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 2, total_bytes: 4 } } })))
    expect(items).toHaveBeenCalledTimes(1)
    if (ending === 'Drawer close') fireEvent.click(within(drawer).getByRole('button', { name: 'Close' }))
    if (ending === 'job deletion') act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'stopped', sequence: 2, at: timestamp, payload: { job_id: failedJob.id, status: 'deleted' } } })))
    if (ending === 'unmount') view.unmount()
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(1)
  })

  it('keeps route cards visible when client or transfer-server references fail independently', async () => {
    vi.mocked(api.routes).mockResolvedValue([route])
    vi.mocked(api.clients).mockRejectedValue(new Error('clients offline'))
    renderPage('/routes')
    expect(await screen.findByText('Existing published route')).not.toBeNull()
    expect(screen.getByText('Could not load clients. Published routes remain available.')).not.toBeNull()
  })

  it('keeps receiver controls and routes available when transfer servers fail', async () => {
    vi.mocked(api.routes).mockResolvedValue([route])
    vi.mocked(api.servers).mockRejectedValue(new Error('servers offline'))
    renderPage()
    expect(await screen.findByText('Could not load transfer servers.')).not.toBeNull()
    expect(screen.getByText('Create receive job')).not.toBeNull()
    await userEvent.click(screen.getByRole('tab', { name: 'Published routes' }))
    expect(await screen.findByText('Existing published route')).not.toBeNull()
  })

  it('compare-and-clears only the capability generation that was explicitly confirmed', async () => {
    vi.mocked(api.transferShares).mockResolvedValue([readyShare])
    const replacement = deferred<{ capability: string }>()
    vi.spyOn(api, 'rotateTransferShare').mockResolvedValueOnce({ capability: 'tcs1.old-code' }).mockReturnValueOnce(replacement.promise)
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByText('Rotate code'))
    await user.click(within(Array.from(document.querySelectorAll('.ant-popconfirm')).at(-1) as HTMLElement).getByText('Rotate code'))
    expect(await screen.findByText('tcs1.old-code')).not.toBeNull()
    const old = { shareID: readyShare.id, value: 'tcs1.old-code', generation: 1 }
    const rotateButton = () => Array.from(document.querySelectorAll('.transfer-history button')).find((button) => button.textContent?.includes('Rotate code')) as HTMLButtonElement | undefined
    const secondRotate = rotateButton()
    if (!secondRotate) throw new Error('Rotate button missing')
    await user.click(secondRotate)
    await user.click(within(Array.from(document.querySelectorAll('.ant-popconfirm')).at(-1) as HTMLElement).getByText('Rotate code'))
    replacement.resolve({ capability: 'tcs1.new-code' })
    expect(await screen.findByText('tcs1.new-code')).not.toBeNull()
    const current = { shareID: readyShare.id, value: 'tcs1.new-code', generation: 2 }
    expect(compareAndClearOneTimeCode(current, old)).toEqual(current)
    expect(compareAndClearOneTimeCode(current, current)).toBeNull()
    await user.click(screen.getByText('I saved the code'))
    expect(screen.queryByText('tcs1.new-code')).toBeNull()
  })

  it('clears terminal lifecycle state on retry, rejects stale derivation, re-derives once on completion, and prunes deletion', async () => {
    const runningAfterRetry = { ...failedJob, status: 'running' as const, error_code: undefined }
    const completedAfterRetry = { ...runningAfterRetry, status: 'completed' as const, received_bytes: 4, finished_at: timestamp }
    const terminalItem = { id: 'terminal-item', job_id: failedJob.id, virtual_path: 'terminal.txt', size: 4, status: 'completed' as const, received_bytes: 4, completed_blocks: 1, mtime: timestamp, created_at: timestamp, updated_at: timestamp, finished_at: timestamp }
    const staleItems = deferred<typeof terminalItem[]>()
    let itemCall = 0
    const items = vi.mocked(api.transferJobItems).mockImplementation(async (jobID) => {
      if (jobID !== failedJob.id) return []
      itemCall += 1
      if (itemCall === 1) return [terminalItem]
      if (itemCall === 2) return staleItems.promise
      return [terminalItem]
    })
    vi.mocked(api.transferJobs).mockResolvedValue([failedJob])
    vi.spyOn(api, 'retryTransferJob').mockResolvedValue(runningAfterRetry)
    renderPage()
    const user = userEvent.setup()
    await user.click((await screen.findAllByText('File details'))[0]!)
    await waitFor(() => expect(document.querySelector('.ant-drawer')?.textContent).toContain('1 / 1 files'))
    await user.keyboard('{Escape}')
    await waitFor(() => expect(document.querySelector('.ant-drawer-open')).toBeNull())
    await user.click((await screen.findAllByText('File details'))[0]!)
    await waitFor(() => expect(items).toHaveBeenCalledTimes(2))
    vi.mocked(api.transferJobs).mockResolvedValue([runningAfterRetry])
    await user.click(within(document.querySelector('.ant-drawer') as HTMLElement).getByText('Retry transfer'))
    await waitFor(() => expect(document.querySelector('.ant-drawer')?.textContent).toContain('Running'))
    await waitFor(() => expect(document.querySelector('.ant-drawer')?.textContent).toContain('File progress unavailable'))
    await user.keyboard('{Escape}')
    staleItems.resolve([terminalItem])
    await act(async () => { await Promise.resolve(); await Promise.resolve() })
    expect(document.body.textContent).toContain('File progress unavailable')
    expect(document.body.textContent).not.toContain('1 / 1 files')

    vi.useFakeTimers()
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'running', sequence: 1, at: timestamp, payload: { job_id: failedJob.id, status: 'running', received_bytes: 2, total_bytes: 4, completed_files: 1, total_files: 4 } } })))
    expect(document.body.textContent).toContain('1 / 4 files')
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })

    items.mockClear()
    vi.mocked(api.transferJobs).mockResolvedValue([completedAfterRetry])
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'ready', sequence: 2, at: timestamp, payload: { job_id: failedJob.id, status: 'completed', received_bytes: 4, total_bytes: 4 } } })))
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(1)
    expect(document.body.textContent).toContain('1 / 1 files')
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'ready', sequence: 3, at: timestamp, payload: { job_id: failedJob.id, status: 'completed', received_bytes: 4, total_bytes: 4 } } })))
    await act(async () => { vi.advanceTimersByTime(100); await Promise.resolve(); await Promise.resolve() })
    expect(items).toHaveBeenCalledTimes(1)
    act(() => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: failedJob.id, operation_id: failedJob.id, phase: 'stopped', sequence: 4, at: timestamp, payload: { job_id: failedJob.id, status: 'deleted' } } })))
    expect(document.body.textContent).not.toContain('terminal.txt')
    expect(screen.queryByText('Retry transfer')).toBeNull()
  })

  it('owns exactly one initially empty live region and announces only accepted status transitions', async () => {
    renderPage()
    await screen.findByRole('tab', { name: 'Transfers', selected: true })
    await screen.findByText('Start transfer')
    const regions = document.querySelectorAll('.transfer-live-announcement[aria-live="polite"]')
    expect(regions.length).toBe(1)
    expect(regions[0]?.textContent).toBe('')
    const emit = (sequence: number, status: 'running' | 'completed', received: number) => window.dispatchEvent(new CustomEvent(transferEvent, { detail: { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: readyJob.id, operation_id: readyJob.id, phase: status === 'running' ? 'running' : 'ready', sequence, at: timestamp, payload: { job_id: readyJob.id, status, received_bytes: received, total_bytes: 4 } } }))
    act(() => emit(1, 'running', 1))
    expect(regions[0]?.textContent).toBe('Receive job: Running')
    act(() => emit(2, 'running', 2))
    expect(regions[0]?.textContent).toBe('Receive job: Running')
    act(() => emit(2, 'completed', 4))
    expect(regions[0]?.textContent).toBe('Receive job: Running')
    act(() => emit(3, 'completed', 4))
    expect(regions[0]?.textContent).toBe('Receive job: Completed')
  })
})
