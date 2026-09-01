# Tasks

- [x] Foundation: module, config, Ent schemas, encryption, bootstrap
  - Acceptance: pure-Go binary starts and migrates SQLite
  - Verify: `go test -race ./... && CGO_ENABLED=0 go build ./cmd/tailcat-webui`
- [x] Identity: OIDC, session cookie, ownership middleware
  - Acceptance: valid callback provisions user; cross-owner access is hidden
  - Verify: auth and HTTP contract tests
- [x] Tailcat runtime: multiple servers/clients and full operation adapters
  - Acceptance: independent instances start/stop and expose current status
  - Verify: runtime unit and hermetic integration tests
- [x] Publishing and management API
  - Acceptance: private/public HTTP, SSE and WebSocket subroutes work
  - Verify: reverse-proxy and API tests
- [x] Ant Design web console
  - Acceptance: all core workflows work at desktop/mobile in zh-CN/en-US and
    light/dark/system themes
  - Verify: lint, tests, build and browser walkthrough
- [x] Delivery
  - Acceptance: CI, releases, GHCR image, bilingual docs and real screenshots
  - Verify: `make verify`, Docker build and workflow syntax review
  - Status: local gates, workflow syntax and the first remote CI/Docker build
    pass on `main`

## Gonc-inspired operations extension

- [x] Runtime phases, typed event envelope and exhaustive UI labels
- [x] Injectable Tailcat server/client runtime adapter
- [x] Route-scoped reusable HTTP transport registry and invalidation
- [x] Port-aware operator target-rule parser and resolver
- [x] Owner-scoped exit-node rule persistence and runtime enforcement
- [x] Server policy management UI in English and Chinese
- [x] Diagnostic schema and bounded reserved-port protocol
- [x] Diagnostic lifecycle, API, retention and audit integration
- [x] Client diagnostics tab with progress, history and cancellation
- [x] Transfer share/job/file/item schemas and migrations
- [x] Rooted staging storage and BLAKE3 block manifests
- [x] Capability protocol, resume runner and reserved Tailcat service
- [x] Share/transfer management API, quotas, cleanup and OpenAPI
- [x] Transfer UI with multi-file upload, progress, retry and downloads
- [ ] Browser QA, security review, release verification and documentation
