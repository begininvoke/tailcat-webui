# Spec: runtime-contracts

## Objective

Stabilize Tailcat WebUI's boundary with the upstream Tailcat package, make every
runtime phase exhaustive across Go/OpenAPI/TypeScript, and reuse safe HTTP
transports for published routes. This module is infrastructure for diagnostics
and transfers; it must not change the Tailcat wire protocol.

## Tech Stack

Go 1.27, Echo v5, Tailcat/Tailscale at the pinned revisions, React 19,
TypeScript 6 and Ant Design 6. No new runtime dependency is required.

## Commands

- Generate: `go generate ./ent`
- Go tests: `go test -race ./internal/tailnet ./internal/publish ./internal/httpapi`
- Frontend tests: `cd web && pnpm lint && pnpm test && pnpm build`
- Full gate: `make verify`

## Project Structure

- `internal/tailnet/runtime.go`: runtime interfaces, phases and factory contract.
- `internal/tailnet/runtime_tailcat.go`: the only concrete runtime adapter.
- `internal/tailnet/manager.go`: persistence, quotas and state transitions only.
- `internal/events/`: versioned event envelope and non-blocking owner broker.
- `internal/publish/transport_registry.go`: route-keyed transport lifecycle.
- `web/src/services/api.ts`: mirrored runtime and event discriminated unions.

## Code Style

```go
type RuntimePhase string

const (
	RuntimePhaseStarting RuntimePhase = "starting"
	RuntimePhaseRunning  RuntimePhase = "running"
	RuntimePhaseError    RuntimePhase = "error"
)
```

Use constructors for runtime adapters, small interfaces owned by their
consumers, `context.Context` as the first parameter and `sync.WaitGroup.Go` for
tracked goroutines.

## Testing Strategy

- Contract tests enumerate every Go phase and compare the OpenAPI/TypeScript
  values through existing representative contract tests.
- Factory fakes exercise start failure, cancellation, delete and restore without
  constructing a real Tailcat engine.
- Transport tests prove reuse within one route, isolation across routes, eviction
  on route/client deletion and no reuse after policy revocation.
- Browser tests verify `error` is labeled Error rather than Idle.

## Boundaries

- Always: preserve upstream imports behind `internal/tailnet`; key pooled
  transports by immutable route ID; retain activity deadlines and admission
  limits on every request.
- Ask first: none, this spec implements the approved extension direction.
- Never: add smux/yamux, a second STUN/MQTT stack, string-parse human logs for
  readiness, or reuse connections across owners/routes.

## Success Criteria

- Manager tests can inject fake server/client runtimes.
- Go, OpenAPI and TypeScript use one exhaustive phase vocabulary.
- Published HTTP resources reuse keep-alive connections per route.
- Deleting a route or client closes idle connections and cancels active work.
- Existing streaming, private grants, WebSocket and per-source limits still pass.

## Open Questions

None. Route ID is the transport registry isolation key.
