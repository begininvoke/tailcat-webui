import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError, runtimePhases, transferErrorCodes, transferEventStatuses, transferStatuses } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('API client', () => {
	it('exports the exhaustive runtime phase vocabulary', () => {
		expect(runtimePhases).toEqual(['idle', 'starting', 'connecting', 'ready', 'running', 'stopping', 'stopped', 'error', 'interrupted'])
	})

  it('unwraps collection responses', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ items: [{ id: 'server-1', name: 'Studio' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const servers = await api.servers()
    expect(servers).toHaveLength(1)
    expect(servers[0]?.name).toBe('Studio')
  })

  it('preserves stable API error codes', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'TARGET_DENIED', message: 'Denied' } }), { status: 403, headers: { 'Content-Type': 'application/json' } })))
    await expect(api.clients()).rejects.toMatchObject({ status: 403, code: 'TARGET_DENIED', message: 'Denied' } satisfies Partial<APIError>)
  })

  it('uses the exit-rule policy contract with exact request bodies', async () => {
    const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      if (path.endsWith('/exit-rules') && !init?.method) {
        return new Response(JSON.stringify({ items: [{ id: 'rule-1', prefix: '10.0.0.0/8' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (path.includes('/exit-node')) {
        return new Response(JSON.stringify({ id: 'server-1', exit_node_enabled: true }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (path.endsWith('/exit-rules') && init?.method === 'POST') {
        return new Response(JSON.stringify({ id: 'rule-1', prefix: '10.0.0.0/8' }), { status: 201, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(null, { status: 204 })
    })
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.exitRules('server-1')).resolves.toEqual([{ id: 'rule-1', prefix: '10.0.0.0/8' }])
    await api.createExitRule('server-1', { prefix: '10.0.0.0/8', start_port: 443, end_port: 443, enabled: true })
    await api.setExitNodeEnabled('server-1', true)
    await api.deleteExitRule('rule-1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/servers/server-1/exit-rules', { credentials: 'same-origin', headers: undefined })
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/servers/server-1/exit-rules', expect.objectContaining({
      credentials: 'same-origin', method: 'POST', body: JSON.stringify({ prefix: '10.0.0.0/8', start_port: 443, end_port: 443, enabled: true }),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/servers/server-1/exit-node', expect.objectContaining({
      credentials: 'same-origin', method: 'POST', body: JSON.stringify({ enabled: true }),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(4, '/api/v1/exit-rules/rule-1', expect.objectContaining({ credentials: 'same-origin', method: 'DELETE' }))
  })

  it('uses the diagnostics contract with exact request bodies', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ id: 'run-1', status: 'running' }), { status: 201, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)

    await api.startDiagnostic('client-1', { kind: 'ping', duration_ms: 500, bytes: 0 })
    await api.startDiagnostic('client-1', { kind: 'throughput', duration_ms: 5000, bytes: 33554432 })
    await api.cancelDiagnostic('run-1')

    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/v1/clients/client-1/diagnostics', expect.objectContaining({
      credentials: 'same-origin', method: 'POST', body: JSON.stringify({ kind: 'ping', duration_ms: 500, bytes: 0 }),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/clients/client-1/diagnostics', expect.objectContaining({
      credentials: 'same-origin', method: 'POST', body: JSON.stringify({ kind: 'throughput', duration_ms: 5000, bytes: 33554432 }),
    }))
    expect(fetchMock).toHaveBeenNthCalledWith(3, '/api/v1/diagnostics/run-1/cancel', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
  })

  it('exports the exhaustive transfer vocabularies', () => {
    expect(transferStatuses).toEqual(['staging', 'ready', 'running', 'completed', 'failed', 'canceled', 'interrupted', 'expired', 'deleting'])
    expect(transferEventStatuses).toEqual([...transferStatuses, 'deleted'])
    expect(transferErrorCodes).toEqual(['transfer_canceled', 'transfer_expired', 'transfer_remote_unavailable', 'transfer_invalid_capability', 'transfer_share_not_found', 'transfer_protocol_invalid', 'transfer_integrity_mismatch', 'transfer_storage_failed', 'transfer_limit_exceeded'])
  })

  it('uses every transfer management route with exact methods, JSON bodies and raw upload headers', async () => {
    const file = new File(['tailcat'], 'notes.txt', { type: 'text/plain' })
    const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      if (!init?.method || init.method === 'GET') return new Response(JSON.stringify({ items: [] }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (init.method === 'DELETE' || path.endsWith('/cancel')) return new Response(null, { status: 204 })
      if (path === '/api/v1/transfers/shares') return new Response(JSON.stringify({ share: { id: 'share-1' }, capability: 'tcs1.once' }), { status: 201, headers: { 'Content-Type': 'application/json' } })
      if (path.endsWith('/rotate')) return new Response(JSON.stringify({ capability: 'tcs1.rotated' }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      return new Response(JSON.stringify({ id: 'resource-1' }), { status: path.endsWith('/files') ? 201 : 200, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)

    await api.transferShares()
    await api.createTransferShare({ server_id: 'server-1' })
    await api.transferShare('share-1')
    await api.transferShareFiles('share-1')
    await api.uploadTransferShareFile('share-1', file, 'folder/notes.txt')
    await api.finalizeTransferShare('share-1')
    await api.rotateTransferShare('share-1')
    await api.deleteTransferShare('share-1')
    await api.transferJobs()
    await api.createTransferJob({ client_id: 'client-1', capability: 'tcs1.secret' })
    await api.transferJob('job-1')
    await api.startTransferJob('job-1')
    await api.cancelTransferJob('job-1')
    await api.retryTransferJob('job-1')
    await api.deleteTransferJob('job-1')
    await api.transferJobItems('job-1')
    await api.transferJobItem('job-1', 'item-1')

    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/v1/transfers/shares', expect.objectContaining({ credentials: 'same-origin', method: 'POST', body: '{"server_id":"server-1"}' }))
    expect(fetchMock).toHaveBeenNthCalledWith(5, '/api/v1/transfers/shares/share-1/files', {
      credentials: 'same-origin', method: 'POST', body: file, headers: { 'Content-Type': 'application/octet-stream', 'X-Tailcat-Virtual-Path': 'folder/notes.txt' },
    })
    expect(fetchMock).toHaveBeenNthCalledWith(6, '/api/v1/transfers/shares/share-1/finalize', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(7, '/api/v1/transfers/shares/share-1/rotate', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(8, '/api/v1/transfers/shares/share-1', expect.objectContaining({ credentials: 'same-origin', method: 'DELETE' }))
    expect(fetchMock).toHaveBeenNthCalledWith(10, '/api/v1/transfers/jobs', expect.objectContaining({ credentials: 'same-origin', method: 'POST', body: '{"client_id":"client-1","capability":"tcs1.secret"}' }))
    expect(fetchMock).toHaveBeenNthCalledWith(12, '/api/v1/transfers/jobs/job-1/start', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(13, '/api/v1/transfers/jobs/job-1/cancel', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(14, '/api/v1/transfers/jobs/job-1/retry', expect.objectContaining({ credentials: 'same-origin', method: 'POST' }))
    expect(fetchMock).toHaveBeenNthCalledWith(15, '/api/v1/transfers/jobs/job-1', expect.objectContaining({ credentials: 'same-origin', method: 'DELETE' }))
    expect(api.transferItemDownloadHref('job-1', 'item-1')).toBe('/api/v1/transfers/jobs/job-1/items/item-1/download')
  })

  it('sends Unicode virtual paths as their exact UTF-8 header bytes', async () => {
    const fetchMock = vi.fn(async (path: string, init?: RequestInit) => {
      void path; void init
      return new Response(JSON.stringify({ id: 'file-1' }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    })
    vi.stubGlobal('fetch', fetchMock)
    await api.uploadTransferShareFile('share-1', new Blob(['data']), '资料/文件.txt')
    const init = fetchMock.mock.calls[0]?.[1]
    const header = (init?.headers as Record<string, string>)['X-Tailcat-Virtual-Path']
    if (!header) throw new Error('virtual path header missing')
    expect(Array.from(header, (character) => character.charCodeAt(0))).toEqual(Array.from(new TextEncoder().encode('资料/文件.txt')))
  })

  it('keeps one-time capability material out of transfer list values', async () => {
    const fetchMock = vi.fn(async () => new Response(JSON.stringify({ items: [{ id: 'share-1', server_id: 'server-1', status: 'ready', total_bytes: 0, file_count: 0, expires_at: '2026-09-01T00:00:00Z', created_at: '2026-09-01T00:00:00Z', updated_at: '2026-09-01T00:00:00Z' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    vi.stubGlobal('fetch', fetchMock)
    const [share] = await api.transferShares()
    expect(share).not.toHaveProperty('capability')
  })
})
