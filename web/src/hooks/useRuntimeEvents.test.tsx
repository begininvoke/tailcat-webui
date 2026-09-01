// @vitest-environment jsdom
import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { diagnosticEvent, parseDiagnosticEvent, parseTransferEvent, runtimeRefreshEvent, transferEvent, useRuntimeEvents } from './useRuntimeEvents'

class EventSourceStub {
  static latest: EventSourceStub | null = null
  readonly listeners = new Map<string, EventListener>()
  readonly close = vi.fn()
  constructor() { EventSourceStub.latest = this }
  addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
  removeEventListener(type: string) { this.listeners.delete(type) }
  emit(type: string, data: string) { this.listeners.get(type)?.(new MessageEvent(type, { data })) }
}

function Harness() { useRuntimeEvents(); return null }

describe('runtime events', () => {
  it('routes only well-formed diagnostic events to the focused channel and cleans up listeners', () => {
    vi.stubGlobal('EventSource', EventSourceStub)
    const runtime = vi.fn()
    const diagnostic = vi.fn()
    window.addEventListener(runtimeRefreshEvent, runtime)
    window.addEventListener(diagnosticEvent, diagnostic)
    const view = render(<Harness />)
    const source = EventSourceStub.latest
    if (!source) throw new Error('EventSource was not created')

    const runID = '01994f1c-85ea-7a0d-bbde-ae5ab8a1da1d'
    const clientID = '01994f1c-b7e5-759d-8b3d-a17ed9f03c9e'
    const valid = { version: 1, type: 'diagnostic', resource_kind: 'diagnostic', resource_id: runID, operation_id: runID, phase: 'running', sequence: 1, at: '2026-08-31T12:00:00Z', payload: { client_id: clientID, kind: 'throughput', status: 'running', progress: 42 } }

    act(() => source.emit('diagnostic', JSON.stringify(valid)))
    expect(diagnostic).toHaveBeenCalledTimes(1)
    expect(runtime).not.toHaveBeenCalled()

    for (const invalid of [
      { ...valid, version: 2 },
      { ...valid, resource_kind: 'client' },
      { ...valid, resource_id: 'not-a-uuid' },
      { ...valid, operation_id: crypto.randomUUID() },
      { ...valid, phase: 'unknown' },
      { ...valid, sequence: 0 },
      { ...valid, sequence: 1.5 },
      { ...valid, at: '2026-08-31' },
      { ...valid, payload: { ...valid.payload, client_id: 'not-a-uuid' } },
      { ...valid, payload: { ...valid.payload, kind: 'trace' } },
      { ...valid, payload: { ...valid.payload, status: 'unknown' } },
      { ...valid, payload: { ...valid.payload, progress: 101 } },
      { ...valid, payload: { ...valid.payload, progress: 1.5 } },
      { ...valid, payload: { ...valid.payload, latency_ms: -1 } },
      { ...valid, payload: { ...valid.payload, error_code: 'secret_server_error' } },
      { ...valid, payload: { ...valid.payload, capability: 'tcs1.secret' } },
      { ...valid, internal: 'not-public' },
    ]) act(() => source.emit('diagnostic', JSON.stringify(invalid)))
    expect(diagnostic).toHaveBeenCalledTimes(1)
    expect(parseDiagnosticEvent(valid)?.payload.progress).toBe(42)

    act(() => source.emit('diagnostic', '{invalid'))
    expect(diagnostic).toHaveBeenCalledTimes(1)
    view.unmount()
    expect(source.listeners.size).toBe(0)
    expect(source.close).toHaveBeenCalledTimes(1)
    window.removeEventListener(runtimeRefreshEvent, runtime)
    window.removeEventListener(diagnosticEvent, diagnostic)
    vi.unstubAllGlobals()
  })

  it('announces every stream open for authoritative focused-resource reconciliation', () => {
    vi.stubGlobal('EventSource', EventSourceStub)
    const opened = vi.fn()
    window.addEventListener('tailcat:runtime-stream-open', opened)
    const view = render(<Harness />)
    const source = EventSourceStub.latest
    if (!source) throw new Error('EventSource was not created')

    act(() => source.emit('open', ''))
    act(() => source.emit('open', ''))
    expect(opened).toHaveBeenCalledTimes(2)

    view.unmount()
    window.removeEventListener('tailcat:runtime-stream-open', opened)
    vi.unstubAllGlobals()
  })

  it('validates and targets transfer events without causing a global refresh', () => {
    vi.stubGlobal('EventSource', EventSourceStub)
    const runtime = vi.fn()
    const transfer = vi.fn()
    window.addEventListener(runtimeRefreshEvent, runtime)
    window.addEventListener(transferEvent, transfer)
    const view = render(<Harness />)
    const source = EventSourceStub.latest
    if (!source) throw new Error('EventSource was not created')
    const id = '2f4c51fa-8d36-4c39-b9e4-4af06fe6189c'
    const valid = { version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: id, operation_id: id, phase: 'running', sequence: 9, at: '2026-09-01T12:00:00Z', payload: { job_id: id, status: 'running', received_bytes: 12, total_bytes: 24, completed_files: 1, total_files: 2 } }

    act(() => source.emit('transfer', JSON.stringify(valid)))
    expect(transfer).toHaveBeenCalledTimes(1)
    expect(runtime).not.toHaveBeenCalled()
    for (const malformed of [
      { ...valid, resource_kind: 'job' },
      { ...valid, operation_id: crypto.randomUUID() },
      { ...valid, resource_id: 'not-a-uuid' },
      { ...valid, sequence: 0 },
      { ...valid, at: 'never' },
      { ...valid, payload: { ...valid.payload, job_id: crypto.randomUUID() } },
      { ...valid, payload: { ...valid.payload, status: 'unknown' } },
      { ...valid, payload: { ...valid.payload, received_bytes: -1 } },
      { ...valid, payload: { ...valid.payload, error_code: 'secret_server_error' } },
      { ...valid, payload: { ...valid.payload, capability: 'tcs1.secret' } },
      { ...valid, internal: 'not-public' },
    ]) act(() => source.emit('transfer', JSON.stringify(malformed)))
    expect(transfer).toHaveBeenCalledTimes(1)
    expect(parseTransferEvent(valid)?.payload.status).toBe('running')

    view.unmount()
    expect(source.listeners.size).toBe(0)
    window.removeEventListener(runtimeRefreshEvent, runtime)
    window.removeEventListener(transferEvent, transfer)
    vi.unstubAllGlobals()
  })
})
