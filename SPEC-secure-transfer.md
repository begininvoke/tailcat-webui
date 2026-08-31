# Spec: secure-transfer

## Objective

Transfer browser-staged files between two Tailcat WebUI peers using immutable,
expiring shares and resumable BLAKE3-verified jobs over a reserved Tailcat TCP
service. No arbitrary host filesystem browsing is permitted.

## Tech Stack

Go 1.27, Ent/SQLite metadata, owner-only staging directories, BLAKE3, the
Tailcat runtime adapter and Ant Design Upload/Progress components. Add only
`github.com/zeebo/blake3` as a direct dependency.

## Commands

- Generate: `go generate ./ent`
- Test: `go test -race ./internal/transfer ./internal/tailnet ./internal/httpapi`
- Frontend: `cd web && pnpm lint && pnpm test && pnpm build`
- Full gate: `make verify`

## Project Structure

- `ent/schema/transfershare.go`, `sharefile.go`, `transferjob.go`,
  `transferitem.go`: normalized owner-scoped metadata and cascade edges.
- `internal/transfer/storage.go`: rooted staging, quota reservation and cleanup.
- `internal/transfer/manifest.go`: 8 MiB BLAKE3 block manifests.
- `internal/transfer/protocol.go`: length-bounded manifest/range protocol.
- `internal/transfer/service.go`: share/job lifecycle and restart recovery.
- `web/src/pages/RoutesPage.tsx`: Routes and Transfers tabs.

## Code Style

```go
type JobStatus string

type FileManifest struct {
	FileID     string
	VirtualPath string
	Size       int64
	BLAKE3     string
	Blocks     []Block
}
```

Construct services with required dependencies, keep filesystem paths private to
`Storage`, accept only IDs and virtual paths at higher layers, and wrap errors
with stable machine codes at the HTTP boundary.

## Data Model and Consistency

SQLite is the source of truth for metadata; the filesystem stores large bytes.
Shares and jobs use explicit `staging`, `ready`, `running`, `completed`,
`failed`, `canceled`, `expired`, and `deleting` states. Uploads write a 0600
temporary file, hash and fsync it, atomically rename it, then commit metadata.
On metadata failure the renamed file is removed. Startup cleanup removes
orphaned temporary files and changes abandoned running jobs to interrupted
before resuming eligible jobs.

The capability code is `tcs1.<payload>`, contains a share UUID plus 32 random
bytes, and is shown only on create/rotate. SQLite stores only SHA-256 of the
secret; token comparisons are constant time. Remote capability codes saved for
resume are encrypted with the existing AES-GCM box and owner/job associated
data.

## Protocol

Reserved Tailcat TCP port `41641` accepts a maximum 8 KiB JSON request frame
with version, share ID, capability, operation, file ID, offset and length.
Operations are `manifest` and `range`. Responses are length-prefixed and capped.
Manifests bind immutable file ID, virtual path, size, mtime, whole-file BLAKE3,
8 MiB block size and block hashes. A completed item is rehashed in full before
the job can complete.

## Testing Strategy

- Path tests cover absolute paths, `..`, NUL, Unicode, sibling prefixes,
  symlink/reparse escapes and depth/length limits.
- Protocol tests cover oversized frames, invalid capability, cross-server share,
  invalid ranges, truncation, replay after rotation and expiration.
- Service tests cover crash states, resume, sparse block repair, final hash
  mismatch, quotas, cancellation, cleanup and cross-owner IDs.
- Browser tests cover multi-file upload, copy-once code, receive progress,
  cancel, retry and file download.

## Boundaries

- Always: browser-selected files only; normalized relative virtual paths;
  random disk names; owner/share/job queries; 24-hour default expiry; audit
  create/rotate/start/cancel/complete/delete.
- Limits: 512 MiB per file, 1 GiB per share/job, 2 GiB staged bytes per owner,
  1,000 files per share, four parallel range workers, two jobs per owner.
- Ask first: none.
- Never: accept a server filesystem path, follow symlinks, expose staging through
  the publish origin, trust size/mtime as integrity, or mark complete without a
  final whole-file hash.

## Success Criteria

- A sender selects a server, stages browser files and receives a rotatable code.
- A receiver selects an existing client, enters the code and can resume verified
  downloads after interruption or restart.
- Expiry/deletion cancels active streams and removes bytes without crossing
  owner directories.
- Capability, file paths and hashes never appear in logs beyond non-sensitive
  IDs and counts.

## Open Questions

None. Native/mobile filesystem agents remain a separate future product.
