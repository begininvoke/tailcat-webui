<p align="center">
  <img src="docs/assets/tailcat.png" alt="Tailcat" width="96">
</p>

<h1 align="center">Tailcat WebUI</h1>

<p align="center">A multi-user Tailcat control plane and responsive web console.</p>

<p align="center">
  <a href="README_ZH.md">简体中文</a> ·
  <a href="https://github.com/ca-x/tailcat-webui/actions/workflows/ci.yml">CI</a> ·
  <a href="docs/openapi.yaml">OpenAPI</a>
</p>

Tailcat WebUI turns [Tailcat](https://github.com/tailscale/tailcat) into a
long-running, OIDC-authenticated application. Each user can operate multiple
independent Tailcat servers and clients, then publish remote HTTP, SSE, or
WebSocket resources below stable subroutes.

## Screenshots

### Server management · desktop light theme

![Tailcat server management](docs/screenshots/server-desktop-light.png)

### Network overview · mobile dark theme in Simplified Chinese

<p align="center">
  <img src="docs/screenshots/mobile-dashboard-dark-zh.png" alt="Tailcat mobile dashboard in dark mode and Simplified Chinese" width="390">
</p>

Both images are captured from the running embedded application, not mockups.

## Capabilities

| Tailcat capability | WebUI implementation |
| --- | --- |
| Pipe stdin/stdout | Authenticated binary WebSocket TCP tunnel |
| Expose local TCP ports | Per-server port mappings with deployment target policy |
| Auth-free SSH server | Available only in explicit loopback demo mode; production uses TCP forwarding to a hardened SSH daemon |
| Ping and direct-path detection | Client ping with direct, DERP, or peer-relay status |
| SOCKS-style arbitrary TCP dialing | Browser TCP tunnel accepts `host:port` through the selected Tailcat client |
| Exit node | Per-server option, constrained by allowed destination CIDRs |
| Parse and resolve tokens | Built-in token tools and API endpoints |
| Ephemeral and saved keys | Per-resource choice; saved private keys are AES-256-GCM encrypted |
| Client allowlist | Named public keys; first enable/revocation safely stops a live server to apply fail-closed policy |
| DNS tokens | Resolves `tailcat=tc…` TXT records when creating clients |
| Custom DERP | Region ID/code, custom host list, or alternate DERP map URL |
| Multiple instances | Independent server/client runtimes in one process and per user |

Additional product features:

- OIDC authorization-code flow with state, nonce, PKCE, server-side sessions,
  HTTP-only cookies, and owner-scoped queries.
- Public or owner-only `/r/{slug}/*` routes with streaming and WebSocket support.
- React 19 + Ant Design 6 interface using framework components for navigation,
  forms, drawers, dialogs, confirmations, tables, and notifications.
- English and Simplified Chinese; light, dark, and system appearance.
- Pure-Go SQLite through Ent and `github.com/lib-x/entsqlite`; no CGO.
- One embedded binary plus Linux amd64/arm64 container images.

## Quick start

Requirements: Go 1.27.0, Node.js 26, and pnpm 11.3.

```sh
git clone https://github.com/ca-x/tailcat-webui.git
cd tailcat-webui
cd web && pnpm install --frozen-lockfile --ignore-scripts && cd ..
make build
```

Create an OIDC client whose callback is:

```text
https://tailcat.example.com/api/v1/auth/callback
```

Then configure and run:

```sh
export TAILCAT_WEBUI_ADDR=:8080
export TAILCAT_WEBUI_BASE_URL=https://tailcat.example.com
export TAILCAT_WEBUI_PUBLISH_BASE_URL=https://publish.tailcat.example.com
export TAILCAT_WEBUI_DATA_DIR=./data
export TAILCAT_WEBUI_MASTER_KEY="$(openssl rand -base64 32)"
export TAILCAT_WEBUI_OIDC_ISSUER=https://id.example.com
export TAILCAT_WEBUI_OIDC_CLIENT_ID=tailcat-webui
export TAILCAT_WEBUI_OIDC_CLIENT_SECRET=replace-me
./bin/tailcat-webui
```

`TAILCAT_WEBUI_MASTER_KEY` must remain stable. It encrypts remote connection
tokens and saved Tailcat private identities; losing it makes those records
unrecoverable.

For a loopback-only evaluation without an identity provider:

```sh
TAILCAT_WEBUI_DEMO_MODE=true make dev
```

Demo mode refuses non-loopback base URLs and listen addresses. `make dev`
supplies a public, development-only master key; never reuse it outside demo.

## Docker

```sh
docker run --rm -p 8080:8080 \
  -v tailcat-data:/data \
  -e TAILCAT_WEBUI_BASE_URL=https://tailcat.example.com \
  -e TAILCAT_WEBUI_PUBLISH_BASE_URL=https://publish.tailcat.example.com \
  -e TAILCAT_WEBUI_MASTER_KEY="$TAILCAT_WEBUI_MASTER_KEY" \
  -e TAILCAT_WEBUI_OIDC_ISSUER=https://id.example.com \
  -e TAILCAT_WEBUI_OIDC_CLIENT_ID=tailcat-webui \
  -e TAILCAT_WEBUI_OIDC_CLIENT_SECRET="$OIDC_CLIENT_SECRET" \
  ghcr.io/ca-x/tailcat-webui:latest
```

Terminate TLS at a trusted reverse proxy and keep
`TAILCAT_WEBUI_BASE_URL=https://…`; this enables Secure session cookies and
HSTS. Configure wildcard DNS/TLS for `*.publish.tailcat.example.com`, route it
and the management hostname to the same listener, and preserve the original
`Host` header. Every published route receives its own immutable-ID subdomain;
this isolates public scripts and private route cookies from other tenants.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `TAILCAT_WEBUI_ADDR` | `127.0.0.1:8080` | HTTP listen address |
| `TAILCAT_WEBUI_BASE_URL` | `http://localhost:8080` | Browser-visible canonical URL |
| `TAILCAT_WEBUI_PUBLISH_BASE_URL` | required outside demo | Separate origin for published resources |
| `TAILCAT_WEBUI_DATA_DIR` | `./data` | SQLite and runtime data directory |
| `TAILCAT_WEBUI_MASTER_KEY` | required outside demo | Base64-encoded 32-byte key for tokens and saved identities |
| `TAILCAT_WEBUI_OIDC_ISSUER` | empty | OIDC discovery issuer |
| `TAILCAT_WEBUI_OIDC_CLIENT_ID` | empty | OIDC client ID |
| `TAILCAT_WEBUI_OIDC_CLIENT_SECRET` | empty | OIDC client secret |
| `TAILCAT_WEBUI_OIDC_SCOPES` | `openid,profile,email` | Requested scopes |
| `TAILCAT_WEBUI_ALLOWED_MAPPING_TARGETS` | loopback CIDRs | Host targets allowed for explicit port mappings |
| `TAILCAT_WEBUI_ALLOWED_EXIT_TARGETS` | empty | Destination CIDRs an exit-node may reach |
| `TAILCAT_WEBUI_TRUSTED_PROXIES` | empty | Proxy CIDRs trusted for `X-Forwarded-For` rate-limit identity |
| `TAILCAT_WEBUI_ALLOWED_DERP_HOSTS` | empty | Extra HTTPS DERP map/relay hosts users may select |
| `TAILCAT_WEBUI_DEMO_MODE` | `false` | Loopback-only development login |
| `TAILCAT_WEBUI_DEMO_UNSAFE_SSH` | `false` | Enable Tailcat's in-process shell only in loopback demo mode |

SQLite uses foreign keys, WAL, `synchronous=NORMAL`, a five-second busy
timeout, mmap, and a bounded connection pool. Shared-cache mode is deliberately
not used because it serializes WAL readers.

## Development

```sh
make generate    # regenerate Ent code
make lint        # Go vet + frontend ESLint
make test        # Go race tests + Vitest
make build       # build web assets and embedded pure-Go binary
make verify      # full local release gate
```

The Go API is split into focused auth, Tailcat runtime, publishing, and HTTP
packages. Direct upstream Tailcat imports are isolated under `internal/tailnet`
because Tailcat does not promise API or wire-format stability.

## Security notes

- Public routes are explicit; new routes default to owner-only.
- Published resources use a separate origin so untrusted remote HTML cannot
  execute with the control-plane origin or register its service worker.
- Every durable lookup includes the authenticated owner ID.
- Management cookies are stripped before requests reach published resources.
- Saved node private keys never appear in API responses or logs.
- Local mappings resolve and pin DNS before dialing to prevent rebinding.
- Exit-node destinations are checked against deployment CIDRs.
- The upstream public Tailcat DERP service is rate-limited and has no SLA;
  production operators should configure their own relay fleet.

See [docs/security.md](docs/security.md) for the threat model.

## License and upstream

Tailcat WebUI is licensed under AGPL-3.0-only. Tailcat and its logo are
BSD-3-Clause software © Tailscale Inc. and contributors. See [NOTICE.md](NOTICE.md).
This project is independent and is not endorsed by Tailscale Inc.
