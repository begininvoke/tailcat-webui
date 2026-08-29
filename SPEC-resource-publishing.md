# Spec: resource-publishing

## Objective

Publish a selected Tailcat client's remote HTTP service below a stable local
path without losing query strings, streaming responses, SSE or WebSocket
upgrades.

Each route is served from an immutable route-ID subdomain beneath a dedicated
publish origin. Management APIs are never served on that origin, preventing
untrusted remote HTML from inheriting control-plane authority.

## Tech Stack

Go `httputil.ReverseProxy` with a Tailcat-backed `http.Transport`, Echo route
handoff and WebSocket-compatible HTTP upgrade tunnelling.

## Commands

```sh
go test -race ./internal/publish/...
```

## Project Structure

```text
internal/publish/    route registry, proxy director and transport
ent/schema/          PublishedRoute
```

## Code Style

```go
type RouteResolver interface {
	ResolveRoute(context.Context, string) (Route, error)
}
```

The proxy depends on a dialer interface, not a concrete Tailcat client.

## Testing Strategy

Cover path-prefix stripping, base-path joining, query preservation, redirects,
stream flushing, WebSocket upgrades, disabled routes, public/private access and
cross-owner denial.

## Boundaries

- Always: validate slugs, strip hop-by-hop headers, set forwarded headers,
  enforce body/header/time limits, partition concurrency by trusted source,
  expire inactive upstream connections and record public-route access metadata.
- Ask first: expose a route anonymously or widen allowed methods.
- Never: accept an arbitrary target URL, proxy management cookies upstream, or
  use host headers to select another tenant's route.

## Success Criteria

- `/r/{slug}/*` reaches the configured Tailcat port and base path.
- Private routes require their owner; public routes work without a session.
- Streaming and WebSocket connections are not buffered by the control plane.
- Active streaming and WebSocket traffic refreshes a five-minute idle deadline;
  one source cannot monopolize a route or the global proxy pool.
