import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError, runtimePhases } from './api'

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
})
