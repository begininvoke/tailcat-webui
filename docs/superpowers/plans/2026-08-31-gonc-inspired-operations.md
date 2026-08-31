# Gonc-inspired operations implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development to implement this plan task-by-task.
> Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add stable runtime contracts, pooled publishing, port-aware policy,
owner-scoped diagnostics and browser-staged verified file transfer to Tailcat
WebUI.

**Architecture:** Keep Tailcat/Tailscale as the only network and identity layer.
SQLite owns metadata while the filesystem owns staged bytes. Live progress is a
droppable typed event stream; durable run/job summaries are authoritative. New
interfaces isolate the upstream Tailcat API and reserved services without
adding gonc's STUN, MQTT, mux, shell or crypto code.

**Tech Stack:** Go 1.27, Echo v5, Ent, pure-Go SQLite, Tailcat/Tailscale, React
19, TypeScript 6, Ant Design 6, i18next and BLAKE3.

**Spec:** `CAPABILITY_MAP.md`, `SPEC-runtime-contracts.md`,
`SPEC-policy-controls.md`, `SPEC-network-diagnostics.md`,
`SPEC-secure-transfer.md`, `SPEC-operations-console.md`

## Global Constraints

- Go version is exactly 1.27.0; apply `use-modern-go` guidance before Go edits.
- Default storage remains pure-Go SQLite and all durable queries include owner
  identity.
- Tailcat/Tailscale remains the only transport; do not add MQTT, STUN, KCP,
  smux, yamux or a second encryption layer.
- Reserved Tailcat TCP ports are `41640` for diagnostics and `41641` for
  transfer; reject persisted user mappings that use either port.
- Diagnostics are limited to 5 seconds and 32 MiB per direction.
- Transfers are limited to 512 MiB/file, 1 GiB/share or job, 2 GiB/owner,
  1,000 files/share, four workers and two concurrent jobs/owner.
- Files are browser-staged only, stored with random disk names under the data
  directory, mode 0600, 24-hour default expiry and final whole-file BLAKE3.
- UI uses existing Ant Design components, English/Chinese copy, light/dark/
  system themes, no native browser dialogs and no sixth primary navigation item.
- Every task follows red-green-refactor, runs its scoped race/frontend tests and
  commits only its files.

---

### Task 1: Runtime phase and event contracts

**Files:**
- Create: `internal/events/envelope.go`
- Modify: `internal/tailnet/manager.go`
- Modify: `docs/openapi.yaml`
- Modify: `web/src/services/api.ts`
- Modify/Test: `web/src/components/RuntimeState.tsx`, `web/src/i18n.test.ts`

**Interfaces:**
- Produces `tailnet.RuntimePhase`, `events.Envelope`, and mirrored TypeScript
  unions consumed by every later task.

- [ ] **Step 1: Add failing contract and UI tests.** Assert the complete values
  `idle, starting, connecting, ready, running, stopping, stopped, error,
  interrupted`, and assert `RuntimeState("error")` renders localized Error.
- [ ] **Step 2: Run the focused tests.** Run
  `go test ./docs ./internal/tailnet && cd web && pnpm test`; expect the new
  phase-contract/UI assertions to fail.
- [ ] **Step 3: Implement closed contracts.** Add:

```go
type RuntimePhase string

type Envelope struct {
	Version      int          `json:"version"`
	Type         string       `json:"type"`
	ResourceKind string       `json:"resource_kind"`
	ResourceID   string       `json:"resource_id"`
	OperationID  string       `json:"operation_id,omitempty"`
	Phase        RuntimePhase `json:"phase"`
	Sequence     uint64       `json:"sequence"`
	At           time.Time    `json:"at"`
	Payload      any          `json:"payload,omitempty"`
}
```

  Keep user ID internal to the owner-partitioned broker and localize stable
  message codes in React.
- [ ] **Step 4: Re-run tests and build.** Run
  `go test -race ./docs ./internal/events ./internal/tailnet` and
  `cd web && pnpm lint && pnpm test && pnpm build`; expect success.
- [ ] **Step 5: Commit.** Commit as
  `feat: add typed runtime event contracts`.

### Task 2: Injectable Tailcat runtime adapter

**Files:**
- Create: `internal/tailnet/runtime.go`
- Create: `internal/tailnet/runtime_tailcat.go`
- Create: `internal/tailnet/runtime_test.go`
- Modify: `internal/tailnet/manager.go`

**Interfaces:**
- Consumes Task 1 phases.
- Produces `RuntimeFactory`, `ServerRuntime`, `ClientRuntime`, `ServerSpec` and
  `ClientSpec` used by Manager and reserved-service tasks.

- [ ] **Step 1: Write failing fake-factory Manager tests.** Cover start failure,
  restore, stop/delete ordering and client close without constructing Tailcat.
- [ ] **Step 2: Run the red tests.** Run
  `go test ./internal/tailnet -run 'TestManager.*RuntimeFactory'`; expect missing
  factory symbols.
- [ ] **Step 3: Implement consumer-owned interfaces.** Required shape:

```go
type RuntimeFactory interface {
	NewServer(context.Context, ServerSpec) (ServerRuntime, error)
	NewClient(context.Context, ClientSpec) (ClientRuntime, error)
}

type ServerRuntime interface {
	Start() error
	Close() error
	DrainTCP(context.Context) error
	ConnectionToken() string
	PublicKey() string
	AddAllowedClient(key.NodePublic)
}
```

  Put all `tailcat.Server`/`tailcat.Client` construction and mutable callbacks in
  `runtime_tailcat.go`. Manager retains persistence, quotas and operation locks.
- [ ] **Step 4: Run runtime and integration tests.** Run
  `go test -race ./internal/tailnet`; expect fake and real multi-instance tests
  to pass.
- [ ] **Step 5: Commit.** Commit as
  `refactor: isolate Tailcat runtime adapters`.

### Task 3: Published route transport registry

**Files:**
- Create: `internal/publish/transport_registry.go`
- Create: `internal/publish/transport_registry_test.go`
- Modify: `internal/publish/service.go`
- Modify: `internal/httpapi/api.go`

**Interfaces:**
- Produces `transportRegistry.Get(RouteTransportKey) *http.Transport`,
  `InvalidateRoute`, `InvalidateClient`, and `Close`.

- [ ] **Step 1: Write failing reuse/isolation tests.** Prove two sequential
  requests for one route use one dial, different routes do not share, and route
  or client invalidation closes idle connections and prevents stale reuse.
- [ ] **Step 2: Run the red tests.** Run `go test ./internal/publish`; expect
  per-request dialing to violate reuse assertions.
- [ ] **Step 3: Implement a bounded registry.** Key by immutable route ID and
  store owner/client/port metadata. Use `http.Transport` with the existing
  activity connection, response-header and idle deadlines. Cap at 512 entries;
  evict least-recently-used idle transports, never active requests.
- [ ] **Step 4: Wire invalidation.** Route delete invalidates its key. Client
  delete asks Publish Service to cancel/evict every owned route before the Ent
  cascade and Tailcat client close.
- [ ] **Step 5: Verify.** Run
  `go test -race ./internal/publish ./internal/httpapi`; expect all streaming,
  WebSocket, grant and delete tests to pass.
- [ ] **Step 6: Commit.** Commit as
  `perf: reuse isolated published route transports`.

### Task 4: Port-aware deployment target rules

**Files:**
- Modify: `internal/tailnet/policy.go`
- Modify: `internal/tailnet/policy_test.go`
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`

**Interfaces:**
- Produces `TargetRule`, `PortRange`, `ParseTargetRules`,
  `TargetPolicy.AllowAddrPort` and `TargetPolicy.Resolve`.

- [ ] **Step 1: Add red table tests.** Cover legacy CIDR, CIDR/domain with one
  port or range, IPv6, IDNA, invalid/wildcard domain, mixed allowed DNS answers
  and numeric-IP pinning.
- [ ] **Step 2: Run tests.** Run
  `go test ./internal/config ./internal/tailnet -run 'Target|Policy|Config'`;
  expect new syntax failures.
- [ ] **Step 3: Implement parser and resolver.** Use `@` for port clauses,
  `idna.Lookup.ToASCII` for exact domains, merge ranges, require every resolved
  address and requested port to match, then return one checked numeric address.
- [ ] **Step 4: Verify compatibility.** Existing `.env.example` bare CIDRs must
  parse unchanged. Run `go test -race ./internal/config ./internal/tailnet`.
- [ ] **Step 5: Commit.** Commit as
  `feat: enforce port-aware target policy`.

### Task 5: Owner-scoped exit rules backend

**Files:**
- Create: `ent/schema/exitrule.go`
- Modify: `ent/schema/tailserver.go`
- Modify: `internal/tailnet/manager.go`
- Modify: `internal/httpapi/api.go`
- Modify/Test: `internal/tailnet/ownership_test.go`, `docs/openapi.yaml`

**Interfaces:**
- Produces `ExitRuleView`, create/list/delete Manager methods and
  `/servers/{id}/exit-rules`, `/exit-rules/{id}` endpoints.

- [ ] **Step 1: Write failing cascade/ownership/intersection tests.** Empty
  tenant rules deny all; enabled rule must also satisfy operator exit policy;
  cross-owner IDs are hidden; deletion stops a running server.
- [ ] **Step 2: Run red tests.** Run
  `go test ./internal/tailnet ./internal/httpapi`; expect missing schema/API.
- [ ] **Step 3: Add Ent schema and regenerate.** Fields are immutable owner and
  server IDs, prefix, start/end port, enabled and timestamps. Add cascade edges,
  run `go generate ./ent` and ensure `git diff -- ent` contains only expected
  generated changes.
- [ ] **Step 4: Implement Manager/API.** Enabling exit-node forwarding requires
  one enabled rule. Build the runtime predicate as deployment-policy AND
  owner-rule. Validate all mutations before stopping a live runtime.
- [ ] **Step 5: Verify.** Run
  `go test -race ./internal/tailnet ./internal/httpapi ./docs`.
- [ ] **Step 6: Commit.** Commit as
  `feat: add owner scoped exit rules`.

### Task 6: Server policy console

**Files:**
- Modify: `web/src/services/api.ts`
- Modify: `web/src/pages/ServersPage.tsx`
- Modify: `web/src/i18n.ts`
- Modify/Test: `web/src/services/api.test.ts`, `web/src/styles.css`

**Interfaces:**
- Consumes Task 5 endpoints.
- Produces an Exit rules tab inside the existing server-settings Drawer.

- [ ] **Step 1: Add failing API and localization tests.** Assert exact request
  bodies and every English/Chinese key.
- [ ] **Step 2: Implement Ant UI.** Use Tabs, Form, Input, InputNumber, Switch,
  Table on desktop, List on mobile, Popconfirm for delete and inline validation.
  Do not add navigation.
- [ ] **Step 3: Verify.** Run
  `cd web && pnpm lint && pnpm test && pnpm build` and inspect at 390/1440 widths.
- [ ] **Step 4: Commit.** Commit as
  `feat: manage exit rules in server console`.

### Task 7: Diagnostic schema and protocol

**Files:**
- Create: `ent/schema/diagnosticrun.go`
- Create: `internal/diagnostics/protocol.go`
- Create: `internal/diagnostics/protocol_test.go`
- Create: `internal/diagnostics/types.go`
- Modify: `ent/schema/user.go`, `ent/schema/tailclient.go`

**Interfaces:**
- Produces `diagnostics.Handler`, `diagnostics.Runner`, run kinds/statuses and
  fixed port `41640`.

- [ ] **Step 1: Add red protocol tests.** Use `net.Pipe` for ping and sequential
  bounded upload/download; assert malformed header, >1 KiB frame, cancellation,
  5-second/32-MiB caps and silent-peer timeout.
- [ ] **Step 2: Add schema and regenerate.** Store summary fields only: owner,
  client, kind, status, path, latency, byte/bps totals, error code, started and
  finished timestamps. Add owner/client cascades.
- [ ] **Step 3: Implement protocol.** Use a versioned fixed magic, JSON header
  bounded to 1 KiB and `io.LimitedReader`. The handler never accepts a target
  address or caller-selected duration/size beyond lower values.
- [ ] **Step 4: Verify.** Run
  `go test -race ./internal/diagnostics ./ent/...`.
- [ ] **Step 5: Commit.** Commit as
  `feat: add bounded diagnostic protocol`.

### Task 8: Diagnostic lifecycle and API

**Files:**
- Create: `internal/diagnostics/service.go`
- Create: `internal/diagnostics/service_test.go`
- Modify: `internal/tailnet/manager.go`
- Modify: `internal/app/app.go`
- Modify: `internal/httpapi/api.go`, `docs/openapi.yaml`

**Interfaces:**
- Consumes runtime factory reserved handlers and Task 7 protocol.
- Produces list/start/cancel endpoints and diagnostic event payloads.

- [ ] **Step 1: Add red lifecycle tests.** Cover owner/client isolation, one run
  per client, two per owner, cancel, restart `running -> interrupted`, audit
  lifecycle and 100-row/30-day retention.
- [ ] **Step 2: Implement Service.** Reserve capacity before goroutines, create
  the durable row before dialing, update progress in memory at most once/second,
  and persist one terminal summary with compare-and-set status.
- [ ] **Step 3: Register reserved service.** Every Tailcat server advertises
  diagnostic port 41640; reject mapping creation on reserved ports. The client
  runner uses only `DialPort` on the selected owner client.
- [ ] **Step 4: Wire API/audit.** Add `/diagnostics`,
  `/clients/{id}/diagnostics`, `/diagnostics/{id}/cancel`. Use stable error codes.
- [ ] **Step 5: Verify.** Run
  `go test -race ./internal/diagnostics ./internal/tailnet ./internal/httpapi`.
- [ ] **Step 6: Commit.** Commit as
  `feat: add owner scoped network diagnostics`.

### Task 9: Client diagnostics console

**Files:**
- Create: `web/src/components/OperationProgress.tsx`
- Modify: `web/src/pages/ClientsPage.tsx`
- Modify: `web/src/services/api.ts`
- Modify/Test: `web/src/i18n.ts`, `web/src/services/api.test.ts`

**Interfaces:**
- Consumes Task 8 API and typed event envelopes.
- Produces Clients/Diagnostics tabs and shared progress display.

- [ ] **Step 1: Add failing behavior tests.** Accessible tab names, start/cancel
  buttons, progress text, direct/relay tags and empty/error states.
- [ ] **Step 2: Implement UI.** Use Tabs, Table/List, Progress, Descriptions and
  one Drawer for starting a run. Use tabular numbers and no decorative chart.
- [ ] **Step 3: Target event refresh.** Refresh only the diagnostics resource for
  matching diagnostic events; do not globally refetch every page.
- [ ] **Step 4: Verify.** Run frontend lint/test/build and inspect English/Chinese
  light/dark at 390/1440.
- [ ] **Step 5: Commit.** Commit as
  `feat: add client diagnostics console`.

### Task 10: Transfer metadata schemas

**Files:**
- Create: `ent/schema/transfershare.go`
- Create: `ent/schema/sharefile.go`
- Create: `ent/schema/transferjob.go`
- Create: `ent/schema/transferitem.go`
- Modify: `ent/schema/user.go`, `tailserver.go`, `tailclient.go`

**Interfaces:**
- Produces normalized entities and cascades consumed by transfer storage/service.

- [ ] **Step 1: Add schema/cascade tests.** Assert every owner edge, server/client
  cascade, share-file/job-item cascade and sensitive capability fields.
- [ ] **Step 2: Define schemas.** Use immutable UUIDs and owner IDs, enumerated
  states from the spec, byte counters, expiry, random local name, virtual path,
  block size/hashes and encrypted remote capability.
- [ ] **Step 3: Generate and verify.** Run `go generate ./ent`,
  `go test ./ent/... ./internal/tailnet -run Cascade`, and inspect the migration.
- [ ] **Step 4: Commit.** Commit as
  `feat: add transfer metadata schema`.

### Task 11: Rooted storage and BLAKE3 manifests

**Files:**
- Create: `internal/transfer/storage.go`
- Create: `internal/transfer/storage_test.go`
- Create: `internal/transfer/manifest.go`
- Create: `internal/transfer/manifest_test.go`
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces `Storage`, `Manifest`, `FileManifest`, `Block` and quota reservation.

- [ ] **Step 1: Add red security tests.** Absolute/parent/NUL/sibling-prefix,
  symlink file/directory, Windows volume/device paths, 1,024-byte/32-depth path,
  file/share/owner quota, interrupted temp and atomic publish.
- [ ] **Step 2: Add BLAKE3 dependency explicitly.** Run
  `go get github.com/zeebo/blake3@v0.2.4` and inspect `go.mod/go.sum`.
- [ ] **Step 3: Implement rooted storage.** Never join request paths to disk.
  Generate random local names, open 0600 exclusive temp files inside validated
  owner/share directories, stream hash, fsync, rename and return metadata.
- [ ] **Step 4: Implement immutable manifests.** Block size is exactly 8 MiB;
  cap hashing workers at min(GOMAXPROCS, 4); whole-file and block hashes are
  lowercase BLAKE3-256.
- [ ] **Step 5: Verify.** Run `go test -race ./internal/transfer` and
  `go mod verify`.
- [ ] **Step 6: Commit.** Commit as
  `feat: add secure transfer storage and manifests`.

### Task 12: Transfer protocol, service and reserved runtime

**Files:**
- Create: `internal/transfer/protocol.go`
- Create: `internal/transfer/protocol_test.go`
- Create: `internal/transfer/service.go`
- Create: `internal/transfer/service_test.go`
- Modify: `internal/tailnet/runtime.go`, `manager.go`, `internal/app/app.go`

**Interfaces:**
- Produces transfer reserved handler, share lifecycle and resumable job runner.

- [ ] **Step 1: Add red protocol/security tests.** Cover 8 KiB frame limit,
  capability hashing, constant-time mismatch, server/share binding, rotation,
  expiry, invalid ranges, truncated responses and cross-owner IDs.
- [ ] **Step 2: Implement protocol.** Fixed port 41641; operations `manifest` and
  `range`; length-prefix every response; use `io.LimitedReader` and exact file
  IDs, never paths, for range reads.
- [ ] **Step 3: Implement job runner.** Four workers request missing 8 MiB blocks,
  write at offsets, update durable item progress at bounded intervals and rehash
  the complete file before terminal success. Context cancellation closes every
  connection and waits for workers.
- [ ] **Step 4: Implement recovery/cleanup.** Startup marks abandoned work
  interrupted, resumes eligible jobs, deletes expired shares/jobs and orphaned
  temps, and enforces two active jobs/owner.
- [ ] **Step 5: Verify.** Run
  `go test -race ./internal/transfer ./internal/tailnet ./internal/app`.
- [ ] **Step 6: Commit.** Commit as
  `feat: transfer staged files over Tailcat`.

### Task 13: Share and transfer management API

**Files:**
- Modify: `internal/httpapi/api.go`
- Create: `internal/httpapi/transfer_test.go`
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `docs/openapi.yaml`, `docs/openapi_test.go`

**Interfaces:**
- Produces share create/list/upload/rotate/delete and transfer
  create/list/cancel/retry/download endpoints.

- [ ] **Step 1: Add red API tests.** Cover unknown fields, owner hiding, body/file
  size, upload path, quota conflicts, one-time create/rotate code, capability not
  returned by list, download headers and mutation audit.
- [ ] **Step 2: Add configuration.** Parse byte sizes and retention with defaults
  from the spec; reject unsafe zero/unbounded values outside demo tests.
- [ ] **Step 3: Implement endpoints.** Skip the global 1 MiB body limit only for
  authenticated upload; immediately wrap with the configured per-file
  `MaxBytesReader`, read deadline, owner capacity reservation and mutation rate
  limit. Download requires completed owner item.
- [ ] **Step 4: Verify OpenAPI and tests.** Run
  `go test -race ./internal/httpapi ./internal/config ./docs`.
- [ ] **Step 5: Commit.** Commit as
  `feat: expose secure transfer management API`.

### Task 14: Transfer console

**Files:**
- Create: `web/src/components/TransferProgress.tsx`
- Modify: `web/src/pages/RoutesPage.tsx`
- Modify: `web/src/services/api.ts`, `web/src/services/api.test.ts`
- Modify: `web/src/i18n.ts`, `web/src/styles.css`

**Interfaces:**
- Consumes Task 13 API/events.
- Produces Routes/Transfers tabs, sender upload and receiver job workflows.

- [ ] **Step 1: Add failing behavior/localization tests.** Cover Ant Upload
  multi-file queue, visible limits, one-time code copy, receive form, cancel,
  retry, completed download, expired and hash-failure states.
- [ ] **Step 2: Implement sender UI.** Use Tabs and Upload.Dragger with
  `beforeUpload={() => false}`, explicit per-file fetch upload and aggregate
  Progress. Capability is displayed in a non-dismissible result Alert until the
  user confirms it is copied; rotation uses Popconfirm.
- [ ] **Step 3: Implement receiver UI.** Select an existing client, enter code,
  show file/byte progress and terminal downloads. Use Table desktop and List
  mobile; progress state includes text and color.
- [ ] **Step 4: Verify UI.** Run lint/test/build and embedded browser QA at
  320/390/768/1440, both locales/themes, keyboard, reduced motion and axe.
- [ ] **Step 5: Commit.** Commit as
  `feat: add verified transfer console`.

### Task 15: Documentation and release gate

**Files:**
- Modify: `README.md`, `README_ZH.md`
- Modify: `docs/security.md`, `docs/qa/browser-qa.md`
- Modify: `.github/workflows/ci.yml`, `tasks/todo.md`
- Modify: `design-system/tailcat-webui/MASTER.md`

**Interfaces:**
- Consumes all previous tasks and closes the delivery ledger.

- [ ] **Step 1: Document topology and limits.** Explain browser staging,
  reserved ports, share code handling, retention, quotas, diagnostics and target
  rule syntax in both READMEs and security model.
- [ ] **Step 2: Add CI target compile gate.** Build the five release targets on
  pull requests without publishing; preserve frozen frontend installs and the
  Linux container build.
- [ ] **Step 3: Capture real screenshots.** Update desktop light and mobile dark
  screenshots to include Diagnostics/Transfers while retaining the Tailcat logo.
- [ ] **Step 4: Run release verification.** Run `make verify`, Actionlint,
  `go mod verify`, pnpm audit/peers, five `CGO_ENABLED=0` cross-builds, browser
  smoke and dependency/secret scans.
- [ ] **Step 5: Request whole-branch review.** Review from the plan base through
  HEAD for spec, security, architecture, UI/accessibility and generated parity;
  fix every Critical/High/Important finding.
- [ ] **Step 6: Commit.** Commit as
  `docs: document diagnostics and secure transfers`.

## Plan self-review

- Spec coverage: every capability-map extension has tasks and verification.
- Placeholder scan: no deferred implementation markers or unspecified error
  handling remain.
- Type consistency: Task 1 contracts feed Tasks 2, 8, 9, 12 and 14; Task 2
  reserved handlers feed diagnostics and transfers; route ID remains the pooled
  transport key; transfer port and limits match all specs.
- Dependency order: Tasks 1-6 can land before diagnostics; Tasks 7-9 precede
  transfer UI; Tasks 10-13 precede Task 14; Task 15 is final.
