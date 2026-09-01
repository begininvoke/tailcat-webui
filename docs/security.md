# Security model

## Assets and trust boundaries

Protected assets are OIDC identities, management sessions, encrypted Tailcat
private keys, remote connection tokens, transfer capability material, staged
file bytes, host-network reachability, and published-route policy. Trust
boundaries exist at browser requests, OIDC redirects, Tailcat peers, proxied
HTTP traffic, runtime target dials, SQLite, the transfer storage root, and
environment configuration.

The management application and published resources never share an origin.
Each published route receives an immutable-ID subdomain below the configured
publish zone and serves its stable `/r/{slug}/*` path there. Private grants are
HMAC scoped to that route and checked against the originating management
session. Logout, session expiry, and revocation therefore remove published
access. Management cookies are stripped before proxying, and published
responses cannot widen their service-worker scope into another route or the
control plane.

## Identity, ownership, and target policy

OIDC uses authorization code, PKCE, state, nonce, issuer, and audience checks.
Sessions are opaque server-side records carried in HttpOnly, SameSite=Lax
cookies. HTTPS deployments also use Secure cookies and HSTS. The application
stores only the provider identity fields needed for account display and
correlation.

Every durable query for servers, clients, mappings, allowlist keys, exit rules,
diagnostics, shares, files, jobs, and downloads includes the authenticated
owner ID. A cross-owner identifier returns not-found instead of revealing
whether the resource exists. Deleting a Tailcat server or client through the
API first invokes owner-and-parent-scoped transfer cleanup. It cancels active
work, removes dependent shares or jobs and staged bytes, and releases quota
before deleting the parent row. A cleanup failure leaves the parent row intact
for retry. Account deletion still cascades database configuration, sessions,
transfer metadata, and encrypted key material. Startup orphan reconciliation
removes staged bytes left without metadata after an abnormal shutdown.

Deployment target rules are the maximum authority. Mapping targets accept
`CIDR`, `CIDR@port`, `CIDR@start-end`, `domain@port`, or `domain@start-end`. A
bare CIDR allows all ports for compatibility. An exact IDNA domain requires an
exact or ranged port clause. All DNS answers must satisfy policy, and the
checked numeric address is pinned for the dial. Exit targets accept only the
three CIDR forms because Tailcat exit forwarding supplies numeric addresses;
domain exit rules are rejected at startup. Owner-scoped exit rules can only
narrow the deployment rules. Empty deployment or owner exit rules deny exit
traffic.

## Network diagnostics

The diagnostics service listens only on reserved Tailcat TCP port `41640`.
Requests select a known owner-scoped client and never carry a host or port.
The protocol limits work to 5 seconds and 32 MiB per direction. The service
permits two active runs per owner and one per client. It retains at most 100
summaries per owner for 30 days and does not store peer IP addresses or live
progress samples.

Protocol frames have fixed bounds and stable error codes. Cancellation closes
the peer connection. Lifecycle transitions are audit logged, while high-rate
progress events remain in memory and may be dropped.

## Transfer capabilities and peer protocol

The secure transfer service listens only on reserved Tailcat TCP port `41641`.
It supports fixed manifest and range operations for an immutable share. It
does not accept a filesystem path or an arbitrary network target.

A share capability contains a UUIDv7 share ID and 32 random bytes. The plaintext
code is returned only when the share is created or rotated. SQLite stores only
the SHA-256 hash of the random secret, and authorization uses a constant-time
comparison, including a dummy comparison for invalid identities. Rotation
closes the old capability generation and active streams before returning the
replacement.

An incoming job keeps the remote capability only to support restart and resume.
The existing AES-256-GCM secret box encrypts it with versioned associated data
that binds the immutable owner ID and job ID. A ciphertext cannot be moved to
another owner or job. Capability plaintext is cleared from mutable buffers when
the operation ends and is excluded from API lists, logs, audit details, and
error responses.

Requests use an 8 KiB maximum JSON frame and bounded responses. Manifests bind
file ID, virtual path, size, modification time, whole-file BLAKE3, 8 MiB block
size, and every block hash. Four range workers verify blocks before recording
progress. A final whole-file BLAKE3 check is required before an item or job can
be completed.

## Browser upload and download boundaries

Only the exact authenticated upload route bypasses the general 1 MiB management
body limit. Authentication, owner lookup, same-origin protection, mutation
rate admission, and share eligibility run before the body is read. Uploads
must use `application/octet-stream`, provide a non-negative `Content-Length`,
and stay within the configured file, share, owner, file-count, and read-deadline
limits. `http.MaxBytesReader` remains the final body ceiling. The UTF-8 virtual
path header is capped at 1,024 bytes and validated as a relative path.
Each sender operation owns an `AbortController`. The drawer and background
progress card can cancel the active upload, and closing the drawer or leaving
the route aborts it. The sender stops before the next queued file and retains
files that were already staged so the same share can be retried.

Downloads are available only for completed owner-scoped items. Responses use
`application/octet-stream`, a sanitized attachment filename,
`X-Content-Type-Options: nosniff`, and `Cache-Control: private, no-store`.
The handler accepts at most one syntactically valid byte range. Multiple,
overflowing, malformed, or unsatisfiable ranges receive `416` without a body.

## Rooted storage, retention, and deletion

Staged bytes live below `<data-dir>/transfers` in owner and share/job roots.
Higher layers pass canonical IDs and virtual paths only. Storage creates random
basenames, private files, and private directories. It rejects absolute paths,
dot segments, NUL and control characters, overlong or over-deep paths, Windows
device names, symlinks, reparse escapes, unsafe hard links, non-regular files,
and a root that changes during use.

Uploads reserve quota before writing, stream into a `0600` temporary file,
hash and fsync it, publish atomically, and sync the containing directory before
metadata commit. Recovery validates file identity through opened handles and
removes orphaned aliases or temporary files without leaving the owner root.
Unix permission and link-count checks are enforced directly. Windows ACL,
reparse-point, hard-link, and directory-sync behavior requires the dedicated
`windows-latest` runtime test; a Linux cross-build cannot prove NTFS semantics.
On Windows, the application also protects the complete data directory before
opening the process lock, SQLite database, or demo master key. Its DACL permits
only the current owner, SYSTEM, and Administrators, and inherited access is
disabled.

Shares and jobs default to a 24-hour lifetime. Operators may tighten the value
from 1 second up to the 24-hour ceiling. Expiry or explicit deletion revokes
active streams, cancels jobs, removes staged bytes, and cascades the matching
metadata. A service-owned scheduler enforces expiry throughout the process
lifetime, wakes for new deadlines, bounds each cleanup batch, and retries
failures. Completed-item downloads hold tracked read leases; deletion closes
and waits for those readers before unlinking data. Configured lifetime and
retention describe the same boundary. Audit records cover every distinct share
create, finalize, rotation and job attempt, including managed resume and
terminal outcome. Audit data contains owner-scoped IDs, counts, outcome, and
stable error codes, never capability text, private keys, file bodies, or whole
virtual paths.

## Resource and deployment controls

Compiled transfer ceilings are 512 MiB per file, 1 GiB per share/job, 2 GiB per
owner, 1,000 files per share, 4,096 retained files per owner, exactly four
workers, and two active jobs per owner. Fixed owner-wide object caps permit 128
retained outgoing shares and 128 retained incoming jobs. Pending object and file
reservations count toward admission, including zero-byte files. Operators may
tighten the configurable byte, per-share file, active-job, and lifetime limits,
but cannot raise the compiled ceilings. Diagnostics and transfer reserved ports
cannot be used for user mappings.

The HTTP server applies header and body limits, timeouts, owner and source rate
limits, and bounded published-route concurrency. The deployment master key is
not stored in SQLite and must remain stable. SQLite uses the pure-Go driver,
and release binaries build with `CGO_ENABLED=0` so filesystem and database
behavior do not depend on a host C toolchain.
