// @vitest-environment jsdom
import { App, ConfigProvider, Grid } from 'antd'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '../i18n'
import { transferEvent } from '../hooks/useRuntimeEvents'
import { APIError, api } from '../services/api'
import RoutesPage, { transferQueueReducer, type QueuedFile } from './RoutesPage'

const { config } = vi.hoisted(() => ({ config: { auth_mode: 'demo' as const, unsafe_ssh: false, version: 'test', transfers: { max_file_bytes: 4, max_share_bytes: 8, max_job_bytes: 8, max_owner_bytes: 16, max_files_per_share: 2, workers: 4 as const, max_jobs_per_owner: 2, expiry_seconds: 86400, retention_seconds: 86400, upload_timeout_seconds: 1800 } } }))
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

function renderPage() {
  return render(<ConfigProvider><App><MemoryRouter initialEntries={['/routes?tab=transfers']}><RoutesPage /></MemoryRouter></App></ConfigProvider>)
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
    let state = transferQueueReducer([], { type: 'add', files: [first, second, first] })
    expect(state.map((item) => item.uid)).toEqual(['upload-1', 'upload-2'])
    state = transferQueueReducer(state, { type: 'status', uid: 'upload-2', status: 'failed', error: 'offline' })
    expect(state[1]).toMatchObject({ uid: 'upload-2', status: 'failed', error: 'offline' })
    state = transferQueueReducer(state, { type: 'remove', uid: 'upload-1' })
    expect(state.map((item) => item.uid)).toEqual(['upload-2'])
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
    expect(upload).toHaveBeenNthCalledWith(1, stagingShare.id, file, 'notes.txt')
    expect(upload).toHaveBeenNthCalledWith(2, stagingShare.id, secondFile, 'second.txt')
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
})
