# Spec: tailcat-runtime

## Objective

Expose Tailcat's current library capabilities through independently managed,
multi-user runtime instances. A user may create multiple server identities and
multiple remote clients; runtime failure in one instance does not stop others.

## Tech Stack

The upstream `github.com/tailscale/tailcat` Go library pinned to an explicit
revision, focused runtime interfaces, per-instance lifecycle contexts and an
in-memory status/event registry backed by Ent configuration.

## Commands

```sh
go test -race ./internal/tailnet/...
go test -count=1 -run TestMultipleTailcatServersInOneProcess ./internal/tailnet
```

## Project Structure

```text
internal/tailnet/    manager, server/client adapters, port proxy and policies
ent/schema/          TailServer, TailClient, PortMapping, AllowedClient
```

## Code Style

```go
type ClientDialer interface {
	DialPort(context.Context, string, uint16) (net.Conn, error)
	Ping(context.Context, string) (Ping, error)
}
```

The concrete Tailcat implementation is created by one factory. Configuration
is represented by required config structs; optional runtime behavior uses
functional options. Cross-cutting logging decorates the focused interfaces.

## Testing Strategy

Use Tailcat's hermetic DERP/STUN integration helpers for multi-instance tests.
Test start-twice, stop-idempotence, independent failures, saved/ephemeral keys,
allowlists, port policy, ping, token parse/resolve and cleanup.

## Boundaries

- Always: one owner per instance, bounded operation contexts, runtime event
  audit, destination policy before every host dial, graceful connection drain.
- Ask first: enable unrestricted host/private-network targets in shared hosting.
- Never: expose saved keys through the API, reuse one identity across owners, or
  silently fall back from a configured allowlist to allow-all.

## Success Criteria

- Multiple servers and clients run concurrently in one process.
- Server modes cover TCP port forwarding, auth-free SSH where supported and
  policy-controlled exit-node forwarding.
- Client modes cover ping/direct-path status, TCP dialing, browser terminal,
  SOCKS session support, token parse/resolve and custom DERP configuration.
- Desired running state is restored at process startup.
