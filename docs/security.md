# Security model

## Assets and trust boundaries

Assets are OIDC identities, sessions, encrypted Tailcat private keys, remote
connection tokens, host-network reachability and public-route policy. Trust
boundaries exist at browser requests, OIDC redirects, Tailcat peers, proxied
HTTP traffic, runtime target dials, SQLite and environment configuration.

Published content never shares the management origin. Each route receives an
immutable-ID subdomain below the configured publish zone; private grants are
HMAC-scoped to that route and continuously checked against the originating
management session, so logout/revocation also revokes published access.

## Primary abuse cases

- A user guesses another user's object ID: every repository query includes the
  authenticated owner ID and returns not-found for mismatches.
- A public route is used as an open proxy: target client, port and base path are
  stored server-side; requests cannot select a host or token.
- Port/exit-node forwarding reaches metadata or another tenant: independent
  deployment policies are evaluated before every dial. Explicit local mappings
  default to loopback, while the exit-node destination allowlist defaults empty.
- A database copy reveals private identities: saved key text is AES-GCM
  ciphertext under a deployment master key not stored in the database.
- An OIDC callback is forged or replayed: short-lived single-use state, nonce,
  PKCE and issuer/audience verification are mandatory.
- A session is stolen by script or cross-site request: opaque HttpOnly cookies,
  SameSite=Lax, origin protection and short idle/absolute expiration.
- Large or slow requests exhaust the process: body/header limits, server
  timeouts, auth rate limits, bounded Tailcat operations, and published-route
  concurrency ceilings at global, owner, route, source, and route/source
  levels. Published upstream connections require activity within five minutes.
- A cross-origin page uses the authenticated TCP tunnel for blind dials: the
  WebSocket handshake and same-origin validation complete before any Tailcat
  target connection is attempted.

## Data minimization

The application stores provider subject, issuer, display name, email and avatar
only for account display and identity correlation. Audit records avoid tokens,
private keys and request bodies. Account deletion cascades owned configuration,
sessions and encrypted key material.
