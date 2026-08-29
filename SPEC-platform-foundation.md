# Spec: platform-foundation

## Objective

Provide one portable Go 1.27 binary with predictable startup and shutdown,
embedded web assets, Ent persistence, pure-Go SQLite by default and encrypted
storage for saved Tailcat identities. Success means the binary runs without
CGO on Linux, macOS and Windows and leaves no runtime goroutines behind on
graceful shutdown.

## Tech Stack

- Go 1.27.0, Echo v5, Ent 0.14, `github.com/lib-x/entsqlite`
- SQLite WAL with private per-connection caches and a bounded pool
- Go `embed` for the Vite production build
- AES-256-GCM for saved private keys; master key supplied by environment

## Commands

```sh
make dev
make test
make lint
make build
docker build -t tailcat-webui:dev .
```

## Project Structure

```text
cmd/tailcat-webui/  minimal executable entry
ent/schema/         durable schema definitions
internal/           configuration, storage and application modules
web/                React source
webdist/            embedded production assets
docs/               architecture, security and API documentation
tasks/              implementation plan and checklist
```

## Code Style

```go
func NewService(store Store, logger *slog.Logger) (*Service, error) {
	if store == nil {
		return nil, errors.New("service: nil store")
	}
	return &Service{store: store, logger: cmp.Or(logger, slog.Default())}, nil
}
```

Constructors enforce invariants, interfaces stay consumer-owned and small,
errors retain context with `%w`, and `main` delegates to one bootstrap path.

## Testing Strategy

Unit tests cover config precedence and encryption. Ent repositories use a
unique in-memory SQLite database per test. Lifecycle tests run with `-race`.
Production build verification uses `CGO_ENABLED=0`.

## Boundaries

- Always: validate config, create runtime directories with restrictive modes,
  run migrations before serving, close runtime instances before the database.
- Ask first: add another database backend or introduce a second process.
- Never: persist plaintext private keys, embed secrets, or require CGO.

## Success Criteria

- `go test -race ./...` and a `CGO_ENABLED=0` build pass.
- SQLite enables foreign keys, WAL, NORMAL synchronous mode and busy timeout.
- Saved-key operations fail closed when no valid master key is configured.
- SPA navigation falls back to embedded `index.html`; `/api` never does.
