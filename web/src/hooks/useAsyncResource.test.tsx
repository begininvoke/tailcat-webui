// @vitest-environment jsdom
import { act, render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useAsyncResource } from './useAsyncResource'
import { runtimeRefreshEvent, runtimeStreamOpenEvent } from './useRuntimeEvents'

function Harness({ load, refreshOnRuntime, refreshOnStreamOpen = false }: { load: () => Promise<string>; refreshOnRuntime: boolean; refreshOnStreamOpen?: boolean }) {
  useAsyncResource(load, { refreshOnRuntime, refreshOnStreamOpen })
  return null
}

function RefreshHarness({ load }: { load: () => Promise<string> }) {
  const resource = useAsyncResource(load, { refreshOnRuntime: false })
  return <><button onClick={() => resource.refresh({ silent: true })}>Refresh</button><output>{resource.data ?? 'pending'}</output></>
}

function deferred<T>() {
  let resolve: (value: T) => void = () => undefined
  const promise = new Promise<T>((nextResolve) => { resolve = nextResolve })
  return { promise, resolve }
}

describe('useAsyncResource', () => {
  it('keeps existing runtime refresh behavior by default and lets focused resources opt out', async () => {
    const defaultLoad = vi.fn(async () => 'default')
    const focusedLoad = vi.fn(async () => 'focused')
    render(<><Harness load={defaultLoad} refreshOnRuntime /><Harness load={focusedLoad} refreshOnRuntime={false} /></>)
    await waitFor(() => expect(defaultLoad).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(focusedLoad).toHaveBeenCalledTimes(1))

    act(() => window.dispatchEvent(new Event(runtimeRefreshEvent)))
    await waitFor(() => expect(defaultLoad).toHaveBeenCalledTimes(2))
    expect(focusedLoad).toHaveBeenCalledTimes(1)
  })

  it('lets focused resources reconcile from the API on every SSE open', async () => {
    const load = vi.fn(async () => 'focused')
    render(<Harness load={load} refreshOnRuntime={false} refreshOnStreamOpen />)
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1))

    act(() => window.dispatchEvent(new Event(runtimeStreamOpenEvent)))
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2))
  })

  it('rejects a stale resource completion after a silent refresh', async () => {
    const first = deferred<string>()
    const second = deferred<string>()
    const load = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const view = render(<RefreshHarness load={load} />)
    await waitFor(() => expect(load).toHaveBeenCalledTimes(1))

    act(() => view.getByRole('button', { name: 'Refresh' }).click())
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2))
    await act(async () => second.resolve('newer'))
    await waitFor(() => expect(view.getByRole('status').textContent).toBe('newer'))
    await act(async () => first.resolve('stale'))
    expect(view.getByRole('status').textContent).toBe('newer')
  })
})
