import { afterEach, describe, expect, it, vi } from 'vitest'
import { api, APIError } from './api'

afterEach(() => vi.unstubAllGlobals())

describe('API client', () => {
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
})
