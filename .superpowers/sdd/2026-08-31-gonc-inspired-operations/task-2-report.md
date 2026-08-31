# Task 2 report: Injectable Tailcat runtime adapter

## Status

Complete. Task 2 introduces consumer-owned runtime contracts, injects the
runtime factory into `Manager`, and moves concrete Tailcat server/client
construction, mutable Tailcat callbacks, handler admission, connection
tracking, cancellation, drain, and close behavior into the concrete adapter.

`ServerSpec.ReservedTCPHandlers` is a port-keyed map and is passed as a non-nil
map by `Manager`. Later diagnostics and transfer tasks can populate ports 41640
and 41641 through this contract. Task 2 intentionally does not add the later
Manager mapping rejection.

## Files

- `internal/tailnet/runtime.go`: added `RuntimeFactory`, `ServerRuntime`,
  `ClientRuntime`, `ServerSpec`, `ClientSpec`, `TCPHandler`, and `PingResult`.
- `internal/tailnet/runtime_tailcat.go`: added the sole concrete
  `tailcat.Server`/`tailcat.Client` construction site and callback/lifecycle
  adapter.
- `internal/tailnet/runtime_test.go`: added fake-factory Manager tests for start
  failure, restore, stop/delete ordering, reserved-handler-map propagation, and
  client close.
- `internal/tailnet/manager.go`: replaced concrete runtime ownership with the
  injected interfaces while retaining persistence, quotas, policy checks,
  operation locks, and runtime state behavior.

## TDD evidence

Red command:

```text
go test ./internal/tailnet -run 'TestManager.*RuntimeFactory'
```

Red result: failed to compile on the deliberately missing `ServerRuntime`,
`ServerSpec`, `ClientRuntime`, `ClientSpec`, `PingResult`, and `RuntimeFactory`
symbols.

Green command:

```text
go test ./internal/tailnet -run 'TestManager.*RuntimeFactory'
```

Green result:

```text
ok  github.com/ca-x/tailcat-webui/internal/tailnet  0.015s
```

Race/integration command:

```text
go test -race ./internal/tailnet
```

Race/integration result:

```text
ok  github.com/ca-x/tailcat-webui/internal/tailnet  1.822s
```

This includes the existing hermetic multi-instance Tailcat test.

## Commit

Subject: `refactor: isolate Tailcat runtime adapters`

## Self-review

- `git diff --check` is clean.
- Concrete `tailcat.Server` and `tailcat.Client` literals and assignments to
  `OnTCP`/`OnTCPForward` are confined to `runtime_tailcat.go` (apart from the
  pre-existing upstream multi-instance integration test).
- The adapter clones slice/map configuration before assigning callbacks, rejects
  duplicate user/reserved/SSH port registrations, and advertises all configured
  direct ports through `ServedTCPPorts`.
- Shutdown retains the previous order: stop admission, cancel handlers, close
  tracked connections, wait for handlers, drain Tailcat TCP, then close the
  runtime. Server deletion still clears desired state and closes the runtime
  before deleting the persisted row.
- Auth-free SSH, target-policy resolution, exit forwarding, per-server/client
  quotas, operation locking, allowlist fail-closed behavior, client caching,
  ping, and dial behavior remain represented.

## Concerns

- A broader non-scoped probe, `go test ./internal/app ./internal/publish`, found
  a pre-existing Task 1 compile error in `internal/app/app.go`: concatenating
  `"runtime." + event.State` produces `tailnet.RuntimePhase`, but the audit
  field requires `string`. `internal/publish` passes. Fixing `internal/app` is
  outside Task 2's allowed files and is not included here.
- Reserved ports are exposed through the adapter contract now; registration and
  explicit rejection of user mappings on 41640/41641 remain intentionally
  deferred to the later diagnostics/transfer tasks per the preflight ruling.

## Fix round 1: deterministic adapter cancellation

Status: addressed the runtime-contracts review finding with a deterministic
test at the concrete `tailcatServerRuntime` boundary. The only production seam
added is the internal `tailcatServerEngine` interface and its upstream Tailcat
wrapper; admission, cancellation, tracking, drain, and close behavior remain in
the concrete adapter.

Red command:

```text
go test -count=1 ./internal/tailnet -run '^TestTailcatRuntimeAdapterShutdownCancelsHandlers$'
```

Red result: compilation failed because the recording engine could not be passed
to `newTailcatServerRuntime`, which still required `*tailcat.Server`.

Green command and result:

```text
go test -count=1 ./internal/tailnet -run '^TestTailcatRuntimeAdapterShutdownCancelsHandlers$'
ok  github.com/ca-x/tailcat-webui/internal/tailnet  0.006s
```

Race stress and scoped integration results:

```text
go test -count=50 -race ./internal/tailnet -run '^TestTailcatRuntimeAdapterShutdownCancelsHandlers$'
ok  github.com/ca-x/tailcat-webui/internal/tailnet  1.029s

go test -count=1 -race ./internal/tailnet
ok  github.com/ca-x/tailcat-webui/internal/tailnet  1.823s
```

The test uses channel handshakes to prove all of the following without sleeps
or external network access:

- an admitted handler starts and receives adapter-owned context cancellation;
- shutdown closes the handler's tracked connection;
- a connection arriving after shutdown is closed without running its handler;
- upstream `DrainTCP` is not invoked while the admitted handler is held open;
- after handler exit, the exact observed order is handler exit, `DrainTCP`, then
  `Close`.

Fix commit subject: `test: cover runtime adapter cancellation`.
