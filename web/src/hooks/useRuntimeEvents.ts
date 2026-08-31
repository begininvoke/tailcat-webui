import { useEffect } from 'react'
import { diagnosticErrorCodes, diagnosticKinds, diagnosticStatuses, runtimePhases, type DiagnosticErrorCode, type DiagnosticKind, type DiagnosticStatus, type RuntimeEvent } from '../services/api'

export const runtimeRefreshEvent = 'tailcat:runtime-refresh'
export const diagnosticEvent = 'tailcat:diagnostic'

export interface DiagnosticEventPayload {
  client_id: string; kind: DiagnosticKind; status: DiagnosticStatus; progress: number;
  latency_ms?: number; upload_bytes?: number; download_bytes?: number; upload_bps?: number; download_bps?: number; error_code?: DiagnosticErrorCode;
}

export interface DiagnosticRuntimeEvent extends RuntimeEvent {
  type: 'diagnostic'; resource_kind: 'diagnostic'; operation_id: string; payload: DiagnosticEventPayload;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null
const isOneOf = <T extends readonly string[]>(value: unknown, options: T): value is T[number] => typeof value === 'string' && options.includes(value)
const isNonNegativeInteger = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
const isPositiveInteger = (value: unknown): value is number => isNonNegativeInteger(value) && value > 0

export function parseDiagnosticEvent(value: unknown): DiagnosticRuntimeEvent | null {
  if (!isRecord(value) || value.type !== 'diagnostic' || value.resource_kind !== 'diagnostic' || typeof value.resource_id !== 'string' || value.resource_id.length === 0 || typeof value.operation_id !== 'string' || value.operation_id !== value.resource_id || typeof value.version !== 'number' || !Number.isSafeInteger(value.version) || typeof value.phase !== 'string' || !isOneOf(value.phase, runtimePhases) || !isPositiveInteger(value.sequence) || typeof value.at !== 'string' || Number.isNaN(Date.parse(value.at)) || !isRecord(value.payload)) return null
  const payload = value.payload
  const clientID = payload.client_id
  const kind = payload.kind
  const status = payload.status
  const progress = payload.progress
  if (typeof clientID !== 'string' || clientID.length === 0 || !isOneOf(kind, diagnosticKinds) || !isOneOf(status, diagnosticStatuses) || !isNonNegativeInteger(progress) || progress > 100) return null
  const measurements: Partial<Pick<DiagnosticEventPayload, 'latency_ms' | 'upload_bytes' | 'download_bytes' | 'upload_bps' | 'download_bps'>> = {}
  for (const key of ['latency_ms', 'upload_bytes', 'download_bytes', 'upload_bps', 'download_bps'] as const) {
    const measurement = payload[key]
    if (measurement !== undefined && !isNonNegativeInteger(measurement)) return null
    if (measurement !== undefined) measurements[key] = measurement
  }
  const errorCode = payload.error_code
  if (errorCode !== undefined && !isOneOf(errorCode, diagnosticErrorCodes)) return null
  return {
    version: value.version, type: 'diagnostic', resource_kind: 'diagnostic', resource_id: value.resource_id, operation_id: value.operation_id,
    phase: value.phase, sequence: value.sequence, at: value.at,
    payload: { client_id: clientID, kind, status, progress, ...measurements, ...(errorCode === undefined ? {} : { error_code: errorCode }) },
  }
}

export function useDiagnosticEvents(onDiagnostic: (event: DiagnosticRuntimeEvent) => void) {
  useEffect(() => {
    const listener = (event: Event) => {
      if (!(event instanceof CustomEvent)) return
      const parsed = parseDiagnosticEvent(event.detail)
      if (parsed) onDiagnostic(parsed)
    }
    window.addEventListener(diagnosticEvent, listener)
    return () => window.removeEventListener(diagnosticEvent, listener)
  }, [onDiagnostic])
}

export function useRuntimeEvents() {
  useEffect(() => {
    const source = new EventSource('/api/v1/events', { withCredentials: true })
    const refresh = () => window.dispatchEvent(new Event(runtimeRefreshEvent))
    const diagnostic = (event: Event) => {
      if (!(event instanceof MessageEvent) || typeof event.data !== 'string') return
      try {
        const parsed = parseDiagnosticEvent(JSON.parse(event.data) as unknown)
        if (parsed) window.dispatchEvent(new CustomEvent<DiagnosticRuntimeEvent>(diagnosticEvent, { detail: parsed }))
      } catch { /* malformed event data is ignored */ }
    }
    source.addEventListener('runtime', refresh)
    source.addEventListener('diagnostic', diagnostic)
    source.addEventListener('open', refresh)
    return () => {
      source.removeEventListener('runtime', refresh)
      source.removeEventListener('diagnostic', diagnostic)
      source.removeEventListener('open', refresh)
      source.close()
    }
  }, [])
}
