// @vitest-environment jsdom
import { act, render, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { useAsyncResource } from './useAsyncResource'
import { runtimeRefreshEvent } from './useRuntimeEvents'

function Harness({ load, refreshOnRuntime }: { load: () => Promise<string>; refreshOnRuntime: boolean }) {
  useAsyncResource(load, { refreshOnRuntime })
  return null
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
})
