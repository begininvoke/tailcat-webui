export interface PublicConfig { auth_mode: 'oidc' | 'demo'; unsafe_ssh: boolean; version: string }
export interface User { id: string; email?: string; display_name?: string; avatar_url?: string }

export interface Server {
  id: string; name: string; key_mode: 'ephemeral' | 'saved'; region: string; derp_map_url?: string;
  exit_node_enabled: boolean; allowlist_enabled: boolean; desired_running: boolean; runtime_state: 'running' | 'stopped'; connection_token?: string;
  public_key?: string; started_at?: string; mapping_count: number; allowed_key_count: number; created_at: string; updated_at: string;
}

export interface Client {
  id: string; name: string; derp_map_url?: string; saved_key: boolean; token_hint: string;
  runtime_state: 'idle' | 'ready' | 'error'; public_key?: string; last_ping_ms?: number; last_path?: 'direct' | 'derp' | 'peer-relay';
  last_ping_at?: string; created_at: string; updated_at: string;
}

export interface PortMapping {
  id: string; server_id: string; name: string; kind: 'tcp' | 'no_auth_ssh'; listen_port: number;
  target_host: string; target_port: number; enabled: boolean; created_at: string; updated_at: string;
}

export interface AllowedClient {
  id: string; server_id: string; name: string; public_key: string; created_at: string
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
  deleteServer: (id: string) => request<void>(`/api/v1/servers/${id}`, { method: 'DELETE' }),
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
  parseToken: (token: string) => request<{ parsed: unknown }>('/api/v1/tokens/parse', { method: 'POST', body: JSON.stringify({ token }) }),
  resolveToken: (token: string) => request<{ token: string }>('/api/v1/tokens/resolve', { method: 'POST', body: JSON.stringify({ token }) }),
  routes: () => request<{ items: PublishedRoute[] }>('/api/v1/routes').then((r) => r.items),
  createRoute: (body: object) => request<PublishedRoute>('/api/v1/routes', { method: 'POST', body: JSON.stringify(body) }),
  deleteRoute: (id: string) => request<void>(`/api/v1/routes/${id}`, { method: 'DELETE' }),
}
