# Spec: network-diagnostics

## Objective

Give each owner a bounded history of Tailcat path health and an explicit,
resource-capped peer throughput test without exposing arbitrary network targets.

## Tech Stack

Go 1.27, Ent/SQLite, the runtime event envelope, a reserved Tailcat TCP port,
React 19 and Ant Design 6. No chart library is introduced.

## Commands

- Generate: `go generate ./ent`
- Test: `go test -race ./internal/diagnostics ./internal/tailnet ./internal/httpapi`
- Frontend: `cd web && pnpm lint && pnpm test && pnpm build`

## Project Structure

- `ent/schema/diagnosticrun.go`: authoritative run summary.
- `internal/diagnostics/service.go`: lifecycle, retention and protocol client.
- `internal/diagnostics/protocol.go`: fixed diagnostic protocol and limits.
- `internal/httpapi/api.go`: list/start/cancel endpoints.
- `web/src/pages/ClientsPage.tsx`: Clients and Diagnostics tabs.

## Code Style

```go
type RunKind string
type RunStatus string

type Snapshot struct {
	RunID     string
	Bytes     int64
	BitsPerSec int64
	At        time.Time
}
```

Machine errors are stable codes. Live samples are in-memory and droppable;
SQLite stores start/finish summaries only.

## Testing Strategy

- Hermetic `net.Pipe` protocol tests cover malformed frames, cancellation,
  time/byte ceilings and silent peers.
- Service tests prove owner isolation, two-run owner concurrency, 100-run owner
  retention and restart recovery from `running` to `interrupted`.
- Browser tests cover mobile/desktop tables, progress feedback and cancel.

## Boundaries

- Always: fixed reserved port `41640`; maximum 5 seconds, 32 MiB per direction,
  two active runs per owner and one per client; retain at most 100 runs or 30
  days, whichever is smaller.
- Ask first: none.
- Never: accept an arbitrary speed-test host, persist peer IPs, claim the WebUI
  host NAT type describes a remote browser, or persist every progress tick.

## Success Criteria

- Owners can run ping or duplex speed diagnostics on their clients.
- Summaries include latency, direct/DERP/peer-relay path, bytes and average bps.
- Progress is typed, cancelable and audit logged at lifecycle transitions.
- Diagnostics never create a general-purpose dial or bandwidth endpoint.

## Open Questions

None. NAT classification is out of scope for this iteration.
