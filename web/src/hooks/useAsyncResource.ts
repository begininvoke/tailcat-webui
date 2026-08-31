import { useCallback, useEffect, useRef, useState } from 'react'
import { runtimeRefreshEvent } from './useRuntimeEvents'

export interface AsyncResourceOptions { refreshOnRuntime?: boolean }

export function useAsyncResource<T>(load: () => Promise<T>, { refreshOnRuntime = true }: AsyncResourceOptions = {}) {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [revision, setRevision] = useState(0)
  const requestID = useRef(0)

  const refresh = useCallback(({ silent = false }: { silent?: boolean } = {}) => {
    requestID.current += 1
    if (!silent) setLoading(true)
    setError(null)
    setRevision((value) => value + 1)
  }, [])

  useEffect(() => {
    let active = true
    const currentRequestID = ++requestID.current
    Promise.resolve().then(load).then(
      (value) => { if (active && currentRequestID === requestID.current) setData(value) },
      (nextError) => { if (active && currentRequestID === requestID.current) setError(nextError) },
    ).finally(() => { if (active && currentRequestID === requestID.current) setLoading(false) })
    return () => { active = false }
  }, [load, revision])
  useEffect(() => {
    if (!refreshOnRuntime) return
    const onRuntime = () => refresh()
    window.addEventListener(runtimeRefreshEvent, onRuntime)
    return () => window.removeEventListener(runtimeRefreshEvent, onRuntime)
  }, [refresh, refreshOnRuntime])
  return { data, loading, error, refresh, setData }
}
