# Implementation plan

1. Establish the module, configuration, Ent schemas, encrypted secret store and
   one bootstrap/shutdown path. Verify SQLite and pure-Go compilation.
2. Implement OIDC/session services and owner-scoped repositories. Verify login
   controls and cross-user denial.
3. Wrap Tailcat in a multi-instance manager with server/client lifecycle,
   mappings, ping and token operations. Verify multiple concurrent runtimes.
4. Add the publishing proxy and versioned Echo management API with security
   middleware and OpenAPI contract tests.
5. Build the Ant Design React console feature by feature, then add responsive
   layout, light/dark/system theme and English/Chinese locale switching.
6. Embed the production SPA, add CI/release/container workflows and bilingual
   documentation, capture real desktop/mobile screenshots and run the release
   gate.

Risks: Tailcat has no API stability promise, so pin an exact revision and keep
all direct imports behind `internal/tailnet`; public DERP is rate limited, so
support custom maps; shared-host target access is security-sensitive, so apply
policy before any OS dial.

## Gonc-inspired operations extension

1. Stabilize runtime phases, events and the Tailcat adapter boundary.
2. Reuse route-scoped HTTP transports with explicit invalidation.
3. Add port-aware operator policy and owner-scoped exit rules.
4. Add bounded path diagnostics, traffic summaries and peer speed tests.
5. Add browser-staged shares and resumable verified transfer jobs.
6. Extend the existing Ant Design pages without increasing mobile nav count.

The binding implementation plan is
`docs/superpowers/plans/2026-08-31-gonc-inspired-operations.md`.
