import type { AllowedClient, ExitRule, PortMapping } from '../services/api'

export interface ServerSettingsData {
  mappings: PortMapping[]
  allowedClients: AllowedClient[]
  exitRules: ExitRule[]
}

export interface ServerSettingsState extends ServerSettingsData {
  serverID: string | null
  requestID: number
  loading: boolean
  error: boolean
}

export function emptyServerSettings(requestID = 0): ServerSettingsState {
  return { serverID: null, requestID, mappings: [], allowedClients: [], exitRules: [], loading: false, error: false }
}

export function beginServerSettingsLoad(serverID: string, requestID: number): ServerSettingsState {
  return { serverID, requestID, mappings: [], allowedClients: [], exitRules: [], loading: true, error: false }
}

export function completeServerSettingsLoad(state: ServerSettingsState, requestID: number, data: ServerSettingsData): ServerSettingsState {
  if (state.requestID !== requestID) return state
  return { ...state, ...data, loading: false, error: false }
}

export function failServerSettingsLoad(state: ServerSettingsState, requestID: number): ServerSettingsState {
  if (state.requestID !== requestID) return state
  return { ...state, loading: false, error: true }
}

function isCanonicalIPv4(address: string): boolean {
  const octets = address.split('.')
  return octets.length === 4 && octets.every((octet) => /^(?:0|[1-9]\d{0,2})$/.test(octet) && Number(octet) <= 255)
}

function isValidIPv6(address: string): boolean {
  try {
    const parsed = new URL(`http://[${address}]`)
    return parsed.hostname.startsWith('[') && parsed.hostname.endsWith(']')
  } catch {
    return false
  }
}

export function isValidCIDR(value: string): boolean {
  if (value.trim() !== value) return false
  const parts = value.split('/')
  if (parts.length !== 2) return false
  const [address, length] = parts
  if (!address || !length || !/^(?:0|[1-9]\d{0,2})$/.test(length)) return false
  const ipv6 = address.includes(':')
  const mask = Number(length)
  if (mask > (ipv6 ? 128 : 32)) return false
  return ipv6 ? isValidIPv6(address) : isCanonicalIPv4(address)
}
