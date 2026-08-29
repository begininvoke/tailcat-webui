import { useEffect } from 'react'

export const runtimeRefreshEvent = 'tailcat:runtime-refresh'

export function useRuntimeEvents() {
  useEffect(() => {
    const source = new EventSource('/api/v1/events', { withCredentials: true })
    const refresh = () => window.dispatchEvent(new Event(runtimeRefreshEvent))
    source.addEventListener('runtime', refresh)
    source.addEventListener('open', refresh)
    return () => {
      source.removeEventListener('runtime', refresh)
      source.removeEventListener('open', refresh)
      source.close()
    }
  }, [])
}
