# Spec: policy-controls

## Objective

Add port-aware operator target rules and per-server exit-node rules. Tenant
rules are an additional restriction: they can never widen deployment policy.

## Tech Stack

Go 1.27, Ent, SQLite and Ant Design. Domain normalization uses the already
available `golang.org/x/net/idna` package; no new module is added.

## Commands

- Generate: `go generate ./ent`
- Test: `go test -race ./internal/config ./internal/tailnet ./internal/httpapi`
- Frontend: `cd web && pnpm lint && pnpm test && pnpm build`

## Project Structure

- `internal/tailnet/policy.go`: typed `TargetRule` and `PortRange` compilation.
- `internal/config/config.go`: backward-compatible environment parsing.
- `ent/schema/exitrule.go`: owner/server-scoped narrowing rules.
- `internal/httpapi/api.go`: CRUD endpoints with owner checks.
- `web/src/pages/ServersPage.tsx`: exit-policy tab in server settings.

## Code Style

```go
type TargetRule struct {
	Prefix netip.Prefix
	Host   string
	Ports  []PortRange
}
```

Operator syntax is comma-separated `CIDR@port`, `CIDR@start-end`, or
`domain@port`; legacy bare CIDR means all ports. IPv6 CIDRs remain unambiguous
because `@` separates the port clause.

## Testing Strategy

- Table tests cover IPv4/IPv6, exact IDNA domains, ranges, legacy CIDRs and
  malformed rules.
- DNS tests require every answer to satisfy operator policy and pin the selected
  numeric address.
- Exit-node tests prove empty tenant rules deny all and tenant rules are
  intersected with operator rules.
- Ownership tests hide cross-tenant rule IDs.

## Boundaries

- Always: normalize domains, validate all resolved addresses, dial a checked
  numeric IP, keep deny-by-default for empty exit rules.
- Ask first: none.
- Never: support wildcard tenant domains, allow tenant rules to override the
  operator maximum, or delegate DNS policy to an untrusted upstream proxy.

## Success Criteria

- Mapping creation evaluates host, resolved addresses and destination port.
- Enabling an exit node requires at least one enabled owner-scoped exit rule.
- Revoking a rule safely stops the live server before returning.
- The UI uses Ant forms, tables/lists and Popconfirm, with localized errors.

## Open Questions

None. Existing bare CIDR configuration remains valid and means every port.
