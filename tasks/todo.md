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
- [ ] Delivery
  - Acceptance: CI, releases, GHCR image, bilingual docs and real screenshots
  - Verify: `make verify`, Docker build and workflow syntax review
  - Status: local gates and workflow syntax pass; first remote Docker build is
    pending the initial push to `main`
