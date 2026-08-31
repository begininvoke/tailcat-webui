// @vitest-environment jsdom
import { act, render } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { diagnosticEvent, runtimeRefreshEvent, useRuntimeEvents } from './useRuntimeEvents'

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

    act(() => source.emit('diagnostic', JSON.stringify({ version: 1, type: 'diagnostic', resource_kind: 'diagnostic', resource_id: 'run-1', operation_id: 'run-1', phase: 'running', sequence: 1, at: '2026-08-31T12:00:00Z', payload: { client_id: 'client-1', kind: 'throughput', status: 'running', progress: 42 } })))
    expect(diagnostic).toHaveBeenCalledTimes(1)
    expect(runtime).not.toHaveBeenCalled()

    act(() => source.emit('diagnostic', '{invalid'))
    expect(diagnostic).toHaveBeenCalledTimes(1)
    view.unmount()
    expect(source.listeners.size).toBe(0)
    expect(source.close).toHaveBeenCalledTimes(1)
    window.removeEventListener(runtimeRefreshEvent, runtime)
    window.removeEventListener(diagnosticEvent, diagnostic)
    vi.unstubAllGlobals()
  })
})
