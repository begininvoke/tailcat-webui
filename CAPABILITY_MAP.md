# Capability Map: Tailcat WebUI

Tailcat WebUI is a multi-user control plane for independent Tailcat server and
client runtimes. Every durable object belongs to one OIDC user; public resource
routes are explicit exceptions rather than the default.

| Module id | Responsibility | Depends on |
| --- | --- | --- |
| `platform-foundation` | Go process lifecycle, configuration, Ent storage, encrypted secrets, embedded SPA | — |
| `identity-access` | OIDC login, users, server-side sessions, ownership and audit events | `platform-foundation` |
| `tailcat-runtime` | Multiple Tailcat servers and clients, keys, DERP, allowlists, ping, TCP, SSH, SOCKS and exit-node policy | `platform-foundation` |
| `resource-publishing` | Authenticated or public subroute publication for HTTP, SSE and WebSocket resources | `identity-access`, `tailcat-runtime` |
| `management-api` | Versioned JSON API, validation, errors, health and live runtime events | `identity-access`, `tailcat-runtime`, `resource-publishing` |
| `web-console` | React/Ant Design desktop and mobile console, light/dark/system themes, Chinese and English | `management-api` |
| `release-delivery` | Tests, CI, cross-platform binaries, multi-architecture container, documentation and real screenshots | all modules |
| `runtime-contracts` | Stable Tailcat runtime adapters, exhaustive phases, typed event envelopes and reusable route transports | `tailcat-runtime`, `resource-publishing` |
| `policy-controls` | Port-aware deployment rules and owner-scoped exit-node rules that can only narrow operator policy | `identity-access`, `tailcat-runtime`, `runtime-contracts` |
| `network-diagnostics` | Owner-scoped path history, traffic summaries and bounded Tailcat peer speed tests | `runtime-contracts`, `management-api` |
| `secure-transfer` | Browser-staged immutable shares and resumable verified transfer jobs over a reserved Tailcat service | `identity-access`, `runtime-contracts`, `management-api` |
| `operations-console` | Ant Design diagnostics, policy and transfer workflows without expanding top-level mobile navigation | `policy-controls`, `network-diagnostics`, `secure-transfer`, `web-console` |

Build order: `platform-foundation` → (`identity-access`, `tailcat-runtime`) →
`resource-publishing` → `management-api` → `web-console` → `release-delivery`.

Gonc-inspired extension order: `runtime-contracts` → (`policy-controls`,
`network-diagnostics`) → `secure-transfer` → `operations-console` →
`release-delivery`.
