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

Build order: `platform-foundation` → (`identity-access`, `tailcat-runtime`) →
`resource-publishing` → `management-api` → `web-console` → `release-delivery`.
