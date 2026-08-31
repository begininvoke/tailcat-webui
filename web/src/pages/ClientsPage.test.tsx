// @vitest-environment jsdom
import { App, ConfigProvider } from 'antd'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { act, fireEvent, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import '../i18n'
import type { DiagnosticRuntimeEvent } from '../hooks/useRuntimeEvents'
import { api } from '../services/api'
import ClientsPage from './ClientsPage'

const { useAsyncResource, useDiagnosticEvents } = vi.hoisted(() => ({ useAsyncResource: vi.fn(), useDiagnosticEvents: vi.fn() }))
vi.mock('../hooks/useAsyncResource', () => ({ useAsyncResource }))
vi.mock('../hooks/useRuntimeEvents', async (importOriginal) => ({ ...await importOriginal<typeof import('../hooks/useRuntimeEvents')>(), useDiagnosticEvents }))

const client = { id: 'client-1', name: 'Studio relay', saved_key: true, token_hint: 'tailcat=token', runtime_state: 'ready', created_at: '2026-08-31T12:00:00Z', updated_at: '2026-08-31T12:00:00Z' }
const run = { id: 'run-1', client_id: 'client-1', kind: 'throughput' as const, status: 'running' as const, path: 'direct' as const, upload_bytes: 1024, download_bytes: 2048, upload_bps: 8000, download_bps: 16000, started_at: '2026-08-31T12:00:00Z' }
const resource = { data: [client], loading: false, error: null, refresh: vi.fn(), setData: vi.fn() }
const diagnostics = { data: [run], loading: false, error: null, refresh: vi.fn(), setData: vi.fn() }

function renderPage() {
  return render(<ConfigProvider><App><MemoryRouter><ClientsPage /></MemoryRouter></App></ConfigProvider>)
}

describe('ClientsPage diagnostics', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    useAsyncResource.mockImplementation((load: unknown) => load === api.clients ? resource : diagnostics)
  })
  afterEach(() => vi.useRealTimers())

  it('exposes tabs, an accessible start drawer, status/path text and direct cancel action', async () => {
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByRole('tab', { name: 'Diagnostics' }))
    expect(screen.getByText('Direct')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Cancel diagnostic' })).not.toBeNull()
    await user.click(screen.getByRole('button', { name: 'Start diagnostic' }))
    expect(screen.getByText('Tests use the selected Tailcat client only, for at most five seconds.')).not.toBeNull()
    expect(screen.getByText('Client')).not.toBeNull()
    expect(screen.getByText('Diagnostic type')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Start' })).not.toBeNull()
  })

  it('shows localized diagnostic empty and recovery states', async () => {
    useAsyncResource.mockReset()
    useAsyncResource.mockImplementation((load: unknown) => load === api.clients ? resource : { ...diagnostics, data: [], error: new Error('offline') })
    const user = userEvent.setup()
    renderPage()
    await user.click(screen.getByRole('tab', { name: 'Diagnostics' }))
    expect(screen.getByText('Could not load this page.')).not.toBeNull()
    expect(screen.getByRole('button', { name: 'Try again' })).not.toBeNull()
  })

  it('keeps the latest diagnostic event, ignores stale sequences, and coalesces the targeted refresh', () => {
    vi.useFakeTimers()
    renderPage()
    const listener = useDiagnosticEvents.mock.calls[0]?.[0] as ((event: DiagnosticRuntimeEvent) => void) | undefined
    if (!listener) throw new Error('diagnostic event listener was not registered')
    const event = (sequence: number, uploadBytes: number): DiagnosticRuntimeEvent => ({ version: 1, type: 'diagnostic', resource_kind: 'diagnostic', resource_id: 'run-1', operation_id: 'run-1', phase: 'running', sequence, at: '2026-08-31T12:00:00Z', payload: { client_id: 'client-1', kind: 'throughput', status: 'running', progress: uploadBytes, upload_bytes: uploadBytes } })

    act(() => { listener(event(4, 40)); listener(event(4, 50)); listener(event(3, 30)); listener(event(5, 60)) })
    expect(diagnostics.setData).toHaveBeenCalledTimes(2)
    const first = diagnostics.setData.mock.calls[0]?.[0] as (runs: typeof run[]) => typeof run[]
    const second = diagnostics.setData.mock.calls[1]?.[0] as (runs: typeof run[]) => typeof run[]
    expect(second(first([run]))[0]?.upload_bytes).toBe(60)
    expect(resource.refresh).not.toHaveBeenCalled()
    expect(diagnostics.refresh).not.toHaveBeenCalled()

    act(() => vi.advanceTimersByTime(100))
    expect(diagnostics.refresh).toHaveBeenCalledTimes(1)
  })

  it('cancels a queued diagnostic refresh when unmounted', () => {
    vi.useFakeTimers()
    const view = renderPage()
    const listener = useDiagnosticEvents.mock.calls[0]?.[0] as ((event: DiagnosticRuntimeEvent) => void) | undefined
    if (!listener) throw new Error('diagnostic event listener was not registered')
    act(() => listener({ version: 1, type: 'diagnostic', resource_kind: 'diagnostic', resource_id: 'run-1', operation_id: 'run-1', phase: 'running', sequence: 1, at: '2026-08-31T12:00:00Z', payload: { client_id: 'client-1', kind: 'throughput', status: 'running', progress: 10 } }))
    view.unmount()
    act(() => vi.advanceTimersByTime(100))
    expect(diagnostics.refresh).not.toHaveBeenCalled()
  })

  it.each([
    ['duration underflow', '0'],
    ['duration overflow', '5001'],
    ['duration decimal', '1.5'],
  ])('does not start diagnostics for %s', async (_name, duration) => {
    const user = userEvent.setup()
    const start = vi.spyOn(api, 'startDiagnostic')
    renderPage()
    await user.click(screen.getByRole('tab', { name: 'Diagnostics' }))
    await user.click(screen.getByRole('button', { name: 'Start diagnostic' }))
    const input = screen.getByRole('spinbutton', { name: 'Duration' })
    await user.clear(input)
    await user.type(input, duration)
    fireEvent.blur(input)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(start).not.toHaveBeenCalled())
  })

  it.each([
    ['bytes underflow', '0'],
    ['bytes overflow', '33554433'],
    ['bytes decimal', '1.5'],
  ])('does not start throughput diagnostics for %s', async (_name, bytes) => {
    const user = userEvent.setup()
    const start = vi.spyOn(api, 'startDiagnostic')
    renderPage()
    await user.click(screen.getByRole('tab', { name: 'Diagnostics' }))
    await user.click(screen.getByRole('button', { name: 'Start diagnostic' }))
    fireEvent.click(screen.getByRole('radio', { name: 'Duplex throughput' }))
    const input = screen.getByRole('spinbutton', { name: 'Bytes per direction' })
    await user.clear(input)
    await user.type(input, bytes)
    fireEvent.blur(input)
    await user.click(screen.getByRole('button', { name: 'Start' }))
    await waitFor(() => expect(start).not.toHaveBeenCalled())
  })
})
