# Spec: management-api

## Objective

Provide a stable `/api/v1` contract for the web console and automation clients,
with ownership enforcement, machine-readable errors and live runtime status.

## Tech Stack

Echo v5 route groups, `encoding/json/v2`, request DTO validation, SSE events and
OpenAPI 3.1 documentation.

## Commands

```sh
go test -race ./internal/httpapi/...
```

## Project Structure

```text
internal/httpapi/    handlers, DTOs, middleware and error renderer
docs/openapi.yaml    public contract
```

## Code Style

```go
type APIError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}
```

Handlers decode and validate, services decide, repositories persist. Handlers
never query Ent directly.

## Testing Strategy

Use `httptest` for status/body/header contracts, malformed JSON, size limits,
method mismatch, CSRF/cross-origin rejection, rate limits and ownership matrix.

## Boundaries

- Always: request IDs, recovery, structured logging, security headers, explicit
  body caps, content-type checks and owner-scoped service calls.
- Ask first: breaking `/api/v1` changes or wildcard CORS.
- Never: leak internal errors, accept unknown DTO fields or expose key material.

## Success Criteria

- Owner-scoped list/create/delete and runtime actions exist for servers,
  clients, mappings and routes; immutable identity changes use replacement.
- Auth endpoints, dashboard summary, health/readiness and SSE status work.
- OpenAPI matches handler paths and representative contract tests.
