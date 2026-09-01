export interface PublicTransferConfig {
  max_file_bytes: number; max_share_bytes: number; max_job_bytes: number; max_owner_bytes: number;
  max_files_per_share: number; workers: 4; max_jobs_per_owner: number; expiry_seconds: number;
  retention_seconds: number; upload_timeout_seconds: number;
}
export interface PublicConfig { auth_mode: 'oidc' | 'demo'; unsafe_ssh: boolean; version: string; transfers: PublicTransferConfig }
export interface User { id: string; email?: string; display_name?: string; avatar_url?: string }
export const runtimePhases = ['idle', 'starting', 'connecting', 'ready', 'running', 'stopping', 'stopped', 'error', 'interrupted'] as const
export type RuntimePhase = typeof runtimePhases[number]

export interface RuntimeEvent {
  version: number; type: string; resource_kind: string; resource_id: string; operation_id?: string;
  phase: RuntimePhase; sequence: number; at: string; payload?: unknown;
}

export const transferStatuses = ['staging', 'ready', 'running', 'completed', 'failed', 'canceled', 'interrupted', 'expired', 'deleting'] as const
export type TransferStatus = typeof transferStatuses[number]
export const transferEventStatuses = [...transferStatuses, 'deleted'] as const
export type TransferEventStatus = typeof transferEventStatuses[number]
export const transferErrorCodes = ['transfer_canceled', 'transfer_expired', 'transfer_remote_unavailable', 'transfer_invalid_capability', 'transfer_share_not_found', 'transfer_protocol_invalid', 'transfer_integrity_mismatch', 'transfer_storage_failed', 'transfer_limit_exceeded'] as const
export type TransferErrorCode = typeof transferErrorCodes[number]

export interface TransferShare {
  id: string; server_id: string; status: TransferStatus; total_bytes: number; file_count: number;
  expires_at: string; ready_at?: string; created_at: string; updated_at: string;
}
export interface TransferShareCreated { share: TransferShare; capability: string }
export interface TransferCapabilityRotated { capability: string }
export interface TransferShareFile { id: string; virtual_path: string; size: number; mtime: string; created_at: string }
export interface TransferJob {
  id: string; client_id: string; remote_share_id: string; status: TransferStatus; total_bytes: number; received_bytes: number;
  expires_at: string; error_code?: TransferErrorCode; started_at?: string; finished_at?: string; created_at: string; updated_at: string;
}
export interface TransferItem {
  id: string; job_id: string; virtual_path: string; size: number; status: TransferStatus; received_bytes: number;
  completed_blocks: number; mtime: string; started_at?: string; finished_at?: string; created_at: string; updated_at: string;
}
export interface TransferEventPayload {
  share_id?: string; job_id?: string; item_id?: string; status: TransferEventStatus; received_bytes?: number;
  total_bytes?: number; completed_files?: number; total_files?: number; error_code?: TransferErrorCode;
}

export const diagnosticKinds = ['ping', 'throughput'] as const
export type DiagnosticKind = typeof diagnosticKinds[number]
export const diagnosticStatuses = ['running', 'succeeded', 'failed', 'canceled', 'interrupted'] as const
export type DiagnosticStatus = typeof diagnosticStatuses[number]
export const diagnosticPaths = ['direct', 'derp', 'peer_relay'] as const
export type DiagnosticPath = typeof diagnosticPaths[number]
export const diagnosticErrorCodes = ['diagnostic_canceled', 'diagnostic_timeout', 'diagnostic_invalid_magic', 'diagnostic_header_too_large', 'diagnostic_malformed_header', 'diagnostic_invalid_request', 'diagnostic_limit_exceeded', 'diagnostic_io', 'diagnostic_invalid_runner'] as const
export type DiagnosticErrorCode = typeof diagnosticErrorCodes[number]

export interface DiagnosticRun {
  id: string; client_id: string; kind: DiagnosticKind; status: DiagnosticStatus; path?: DiagnosticPath;
  latency_ms?: number; upload_bytes: number; download_bytes: number; upload_bps: number; download_bps: number;
  error_code?: DiagnosticErrorCode; started_at: string; finished_at?: string;
}

export type StartDiagnosticInput =
  | { kind: 'ping'; duration_ms: number; bytes: 0 }
  | { kind: 'throughput'; duration_ms: number; bytes: number }

export interface Server {
  id: string; name: string; key_mode: 'ephemeral' | 'saved'; region: string; derp_map_url?: string;
  exit_node_enabled: boolean; allowlist_enabled: boolean; desired_running: boolean; runtime_state: RuntimePhase; connection_token?: string;
  public_key?: string; started_at?: string; mapping_count: number; allowed_key_count: number; created_at: string; updated_at: string;
}

export interface Client {
  id: string; name: string; derp_map_url?: string; saved_key: boolean; token_hint: string;
  runtime_state: RuntimePhase; public_key?: string; last_ping_ms?: number; last_path?: 'direct' | 'derp' | 'peer-relay';
  last_ping_at?: string; created_at: string; updated_at: string;
}

export interface PortMapping {
  id: string; server_id: string; name: string; kind: 'tcp' | 'no_auth_ssh'; listen_port: number;
  target_host: string; target_port: number; enabled: boolean; created_at: string; updated_at: string;
}

export interface AllowedClient {
  id: string; server_id: string; name: string; public_key: string; created_at: string
}

export interface ExitRule {
  id: string; server_id: string; prefix: string; start_port: number; end_port: number; enabled: boolean;
  created_at: string; updated_at: string;
}

export interface CreateExitRuleInput {
  prefix: string; start_port: number; end_port: number; enabled: boolean
}

export interface PublishedRoute {
  id: string; client_id: string; name: string; slug: string; remote_port: number; base_path: string;
  access: 'private' | 'public'; enabled: boolean; url: string; created_at: string; updated_at: string;
  allowed_methods: string[];
}

export interface Dashboard {
  servers: { total: number; running: number }
  clients: { total: number; reachable: number }
  routes: { total: number; public: number }
  recent_servers: Server[]
  recent_clients: Client[]
}

export class APIError extends Error {
  constructor(public status: number, public code: string, message: string) { super(message) }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const response = await fetch(path, {
    credentials: 'same-origin',
    ...init,
    headers: init?.body ? { 'Content-Type': 'application/json', ...init.headers } : init?.headers,
  })
  if (!response.ok) {
    let code = 'REQUEST_FAILED'
    let message = response.statusText
    try {
      const body = await response.json() as { error?: { code?: string; message?: string } }
      code = body.error?.code ?? code
      message = body.error?.message ?? message
    } catch { /* response is not JSON */ }
    throw new APIError(response.status, code, message)
  }
  if (response.status === 204) return undefined as T
  return response.json() as Promise<T>
}

function headerByteString(value: string) {
  return Array.from(new TextEncoder().encode(value), (byte) => String.fromCharCode(byte)).join('')
}

export const api = {
  config: () => request<PublicConfig>('/api/v1/config'),
  me: () => request<User>('/api/v1/auth/me'),
  demoLogin: () => request<User>('/api/v1/auth/demo', { method: 'POST' }),
  logout: () => request<void>('/api/v1/auth/logout', { method: 'POST' }),
  dashboard: () => request<Dashboard>('/api/v1/dashboard'),
  servers: () => request<{ items: Server[] }>('/api/v1/servers').then((r) => r.items),
  createServer: (body: object) => request<Server>('/api/v1/servers', { method: 'POST', body: JSON.stringify(body) }),
  startServer: (id: string) => request<Server>(`/api/v1/servers/${id}/start`, { method: 'POST' }),
  stopServer: (id: string) => request<void>(`/api/v1/servers/${id}/stop`, { method: 'POST' }),
  setExitNodeEnabled: (id: string, enabled: boolean) => request<Server>(`/api/v1/servers/${id}/exit-node`, { method: 'POST', body: JSON.stringify({ enabled }) }),
  deleteServer: (id: string) => request<void>(`/api/v1/servers/${id}`, { method: 'DELETE' }),
  exitRules: (serverID: string) => request<{ items: ExitRule[] }>(`/api/v1/servers/${serverID}/exit-rules`).then((r) => r.items),
  createExitRule: (serverID: string, body: CreateExitRuleInput) => request<ExitRule>(`/api/v1/servers/${serverID}/exit-rules`, { method: 'POST', body: JSON.stringify(body) }),
  deleteExitRule: (id: string) => request<void>(`/api/v1/exit-rules/${id}`, { method: 'DELETE' }),
  mappings: (serverID: string) => request<{ items: PortMapping[] }>(`/api/v1/servers/${serverID}/mappings`).then((r) => r.items),
  createMapping: (serverID: string, body: object) => request<PortMapping>(`/api/v1/servers/${serverID}/mappings`, { method: 'POST', body: JSON.stringify(body) }),
  deleteMapping: (id: string) => request<void>(`/api/v1/mappings/${id}`, { method: 'DELETE' }),
  allowedClients: (serverID: string) => request<{ items: AllowedClient[] }>(`/api/v1/servers/${serverID}/allowed-clients`).then((r) => r.items),
  createAllowedClient: (serverID: string, body: object) => request<AllowedClient>(`/api/v1/servers/${serverID}/allowed-clients`, { method: 'POST', body: JSON.stringify(body) }),
  deleteAllowedClient: (id: string) => request<void>(`/api/v1/allowed-clients/${id}`, { method: 'DELETE' }),
  clients: () => request<{ items: Client[] }>('/api/v1/clients').then((r) => r.items),
  createClient: (body: object) => request<Client>('/api/v1/clients', { method: 'POST', body: JSON.stringify(body) }),
  pingClient: (id: string) => request<Client>(`/api/v1/clients/${id}/ping`, { method: 'POST' }),
  deleteClient: (id: string) => request<void>(`/api/v1/clients/${id}`, { method: 'DELETE' }),
  diagnostics: () => request<{ items: DiagnosticRun[] }>('/api/v1/diagnostics').then((r) => r.items),
  startDiagnostic: (clientID: string, body: StartDiagnosticInput) => request<DiagnosticRun>(`/api/v1/clients/${clientID}/diagnostics`, { method: 'POST', body: JSON.stringify(body) }),
  cancelDiagnostic: (id: string) => request<void>(`/api/v1/diagnostics/${id}/cancel`, { method: 'POST' }),
  parseToken: (token: string) => request<{ parsed: unknown }>('/api/v1/tokens/parse', { method: 'POST', body: JSON.stringify({ token }) }),
  resolveToken: (token: string) => request<{ token: string }>('/api/v1/tokens/resolve', { method: 'POST', body: JSON.stringify({ token }) }),
  routes: () => request<{ items: PublishedRoute[] }>('/api/v1/routes').then((r) => r.items),
  createRoute: (body: object) => request<PublishedRoute>('/api/v1/routes', { method: 'POST', body: JSON.stringify(body) }),
  deleteRoute: (id: string) => request<void>(`/api/v1/routes/${id}`, { method: 'DELETE' }),
  transferShares: () => request<{ items: TransferShare[] }>('/api/v1/transfers/shares').then((r) => r.items),
  createTransferShare: (body: { server_id: string }) => request<TransferShareCreated>('/api/v1/transfers/shares', { method: 'POST', body: JSON.stringify(body) }),
  transferShare: (id: string) => request<TransferShare>(`/api/v1/transfers/shares/${id}`),
  transferShareFiles: (id: string) => request<{ items: TransferShareFile[] }>(`/api/v1/transfers/shares/${id}/files`).then((r) => r.items),
  uploadTransferShareFile: async (id: string, body: Blob, virtualPath: string): Promise<TransferShareFile> => {
    const response = await fetch(`/api/v1/transfers/shares/${id}/files`, {
      credentials: 'same-origin', method: 'POST', body,
      headers: { 'Content-Type': 'application/octet-stream', 'X-Tailcat-Virtual-Path': headerByteString(virtualPath) },
    })
    if (!response.ok) {
      let code = 'REQUEST_FAILED'
      let message = response.statusText
      try {
        const errorBody = await response.json() as { error?: { code?: string; message?: string } }
        code = errorBody.error?.code ?? code
        message = errorBody.error?.message ?? message
      } catch { /* response is not JSON */ }
      throw new APIError(response.status, code, message)
    }
    return response.json() as Promise<TransferShareFile>
  },
  finalizeTransferShare: (id: string) => request<TransferShare>(`/api/v1/transfers/shares/${id}/finalize`, { method: 'POST' }),
  rotateTransferShare: (id: string) => request<TransferCapabilityRotated>(`/api/v1/transfers/shares/${id}/rotate`, { method: 'POST' }),
  deleteTransferShare: (id: string) => request<void>(`/api/v1/transfers/shares/${id}`, { method: 'DELETE' }),
  transferJobs: () => request<{ items: TransferJob[] }>('/api/v1/transfers/jobs').then((r) => r.items),
  createTransferJob: (body: { client_id: string; capability: string }) => request<TransferJob>('/api/v1/transfers/jobs', { method: 'POST', body: JSON.stringify(body) }),
  transferJob: (id: string) => request<TransferJob>(`/api/v1/transfers/jobs/${id}`),
  startTransferJob: (id: string) => request<TransferJob>(`/api/v1/transfers/jobs/${id}/start`, { method: 'POST' }),
  cancelTransferJob: (id: string) => request<void>(`/api/v1/transfers/jobs/${id}/cancel`, { method: 'POST' }),
  retryTransferJob: (id: string) => request<TransferJob>(`/api/v1/transfers/jobs/${id}/retry`, { method: 'POST' }),
  deleteTransferJob: (id: string) => request<void>(`/api/v1/transfers/jobs/${id}`, { method: 'DELETE' }),
  transferJobItems: (id: string) => request<{ items: TransferItem[] }>(`/api/v1/transfers/jobs/${id}/items`).then((r) => r.items),
  transferJobItem: (jobID: string, itemID: string) => request<TransferItem>(`/api/v1/transfers/jobs/${jobID}/items/${itemID}`),
  transferItemDownloadHref: (jobID: string, itemID: string) => `/api/v1/transfers/jobs/${jobID}/items/${itemID}/download`,
}
