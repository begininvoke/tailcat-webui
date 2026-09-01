import { useEffect } from 'react'
import { diagnosticErrorCodes, diagnosticKinds, diagnosticStatuses, runtimePhases, transferErrorCodes, transferEventStatuses, type DiagnosticErrorCode, type DiagnosticKind, type DiagnosticStatus, type RuntimeEvent, type TransferEventPayload } from '../services/api'

export const runtimeRefreshEvent = 'tailcat:runtime-refresh'
export const diagnosticEvent = 'tailcat:diagnostic'
export const transferEvent = 'tailcat:transfer'

export interface DiagnosticEventPayload {
  client_id: string; kind: DiagnosticKind; status: DiagnosticStatus; progress: number;
  latency_ms?: number; upload_bytes?: number; download_bytes?: number; upload_bps?: number; download_bps?: number; error_code?: DiagnosticErrorCode;
}

export interface DiagnosticRuntimeEvent extends RuntimeEvent {
  type: 'diagnostic'; resource_kind: 'diagnostic'; operation_id: string; payload: DiagnosticEventPayload;
}

export interface TransferRuntimeEvent extends RuntimeEvent {
  type: 'transfer'; resource_kind: 'transfer'; operation_id: string; payload: TransferEventPayload;
}

const isRecord = (value: unknown): value is Record<string, unknown> => typeof value === 'object' && value !== null
const isOneOf = <T extends readonly string[]>(value: unknown, options: T): value is T[number] => typeof value === 'string' && options.includes(value)
const isNonNegativeInteger = (value: unknown): value is number => typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
const isPositiveInteger = (value: unknown): value is number => isNonNegativeInteger(value) && value > 0
const uuidPattern = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i
const isUUID = (value: unknown): value is string => typeof value === 'string' && uuidPattern.test(value)
const timestampPattern = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/
const hasOnlyKeys = (value: Record<string, unknown>, keys: readonly string[]) => Object.keys(value).every((key) => keys.includes(key))

export function parseDiagnosticEvent(value: unknown): DiagnosticRuntimeEvent | null {
  if (!isRecord(value) || !hasOnlyKeys(value, ['version', 'type', 'resource_kind', 'resource_id', 'operation_id', 'phase', 'sequence', 'at', 'payload']) || value.version !== 1 || value.type !== 'diagnostic' || value.resource_kind !== 'diagnostic' || !isUUID(value.resource_id) || value.operation_id !== value.resource_id || typeof value.phase !== 'string' || !isOneOf(value.phase, runtimePhases) || !isPositiveInteger(value.sequence) || typeof value.at !== 'string' || !timestampPattern.test(value.at) || Number.isNaN(Date.parse(value.at)) || !isRecord(value.payload) || !hasOnlyKeys(value.payload, ['client_id', 'kind', 'status', 'progress', 'latency_ms', 'upload_bytes', 'download_bytes', 'upload_bps', 'download_bps', 'error_code'])) return null
  const payload = value.payload
  const clientID = payload.client_id
  const kind = payload.kind
  const status = payload.status
  const progress = payload.progress
  if (!isUUID(clientID) || !isOneOf(kind, diagnosticKinds) || !isOneOf(status, diagnosticStatuses) || !isNonNegativeInteger(progress) || progress > 100) return null
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

export function parseTransferEvent(value: unknown): TransferRuntimeEvent | null {
  if (!isRecord(value) || !hasOnlyKeys(value, ['version', 'type', 'resource_kind', 'resource_id', 'operation_id', 'phase', 'sequence', 'at', 'payload']) || value.version !== 1 || value.type !== 'transfer' || value.resource_kind !== 'transfer' || !isUUID(value.resource_id) || value.operation_id !== value.resource_id || typeof value.phase !== 'string' || !isOneOf(value.phase, runtimePhases) || !isPositiveInteger(value.sequence) || typeof value.at !== 'string' || !timestampPattern.test(value.at) || Number.isNaN(Date.parse(value.at)) || !isRecord(value.payload) || !hasOnlyKeys(value.payload, ['share_id', 'job_id', 'item_id', 'status', 'received_bytes', 'total_bytes', 'completed_files', 'total_files', 'error_code'])) return null
  const payload = value.payload
  if (!isOneOf(payload.status, transferEventStatuses)) return null
  const shareID = payload.share_id
  const jobID = payload.job_id
  if ((shareID === undefined) === (jobID === undefined)) return null
  if (shareID !== undefined && (!isUUID(shareID) || shareID !== value.resource_id)) return null
  if (jobID !== undefined && (!isUUID(jobID) || jobID !== value.resource_id)) return null
  if (payload.item_id !== undefined && !isUUID(payload.item_id)) return null
  const numbers: Partial<Pick<TransferEventPayload, 'received_bytes' | 'total_bytes' | 'completed_files' | 'total_files'>> = {}
  for (const key of ['received_bytes', 'total_bytes', 'completed_files', 'total_files'] as const) {
    const amount = payload[key]
    if (amount !== undefined && !isNonNegativeInteger(amount)) return null
    if (amount !== undefined) numbers[key] = amount
  }
  if (numbers.total_files !== undefined && numbers.total_files > 1000) return null
  if (numbers.completed_files !== undefined && numbers.total_files !== undefined && numbers.completed_files > numbers.total_files) return null
  if (numbers.received_bytes !== undefined && numbers.total_bytes !== undefined && numbers.received_bytes > numbers.total_bytes) return null
  const errorCode = payload.error_code
  if (errorCode !== undefined && !isOneOf(errorCode, transferErrorCodes)) return null
  return {
    version: 1, type: 'transfer', resource_kind: 'transfer', resource_id: value.resource_id, operation_id: value.operation_id,
    phase: value.phase, sequence: value.sequence, at: value.at,
    payload: { ...(shareID === undefined ? {} : { share_id: shareID }), ...(jobID === undefined ? {} : { job_id: jobID }), ...(payload.item_id === undefined ? {} : { item_id: payload.item_id }), status: payload.status, ...numbers, ...(errorCode === undefined ? {} : { error_code: errorCode }) },
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

export function useTransferEvents(onTransfer: (event: TransferRuntimeEvent) => void) {
  useEffect(() => {
    const listener = (event: Event) => {
      if (!(event instanceof CustomEvent)) return
      const parsed = parseTransferEvent(event.detail)
      if (parsed) onTransfer(parsed)
    }
    window.addEventListener(transferEvent, listener)
    return () => window.removeEventListener(transferEvent, listener)
  }, [onTransfer])
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
    const transfer = (event: Event) => {
      if (!(event instanceof MessageEvent) || typeof event.data !== 'string') return
      try {
        const parsed = parseTransferEvent(JSON.parse(event.data) as unknown)
        if (parsed) window.dispatchEvent(new CustomEvent<TransferRuntimeEvent>(transferEvent, { detail: parsed }))
      } catch { /* malformed event data is ignored */ }
    }
    source.addEventListener('runtime', refresh)
    source.addEventListener('diagnostic', diagnostic)
    source.addEventListener('transfer', transfer)
    source.addEventListener('open', refresh)
    return () => {
      source.removeEventListener('runtime', refresh)
      source.removeEventListener('diagnostic', diagnostic)
      source.removeEventListener('transfer', transfer)
      source.removeEventListener('open', refresh)
      source.close()
    }
  }, [])
}
