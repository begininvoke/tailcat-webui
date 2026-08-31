// @vitest-environment jsdom
import { App, ConfigProvider } from 'antd'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import '../i18n'
import { api } from '../services/api'
import ClientsPage from './ClientsPage'

const { useAsyncResource } = vi.hoisted(() => ({ useAsyncResource: vi.fn() }))
vi.mock('../hooks/useAsyncResource', () => ({ useAsyncResource }))

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
})
