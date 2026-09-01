# Browser QA

Date: 2026-09-01

Artifact: freshly built embedded production binary with `CGO_ENABLED=0`

Browser: headless Chromium through the direct `agent-browser` binary

## Evidence boundaries

This report separates automated test evidence from live network evidence.
Hermetic Go and Vitest suites prove deterministic protocol, lifecycle,
ownership, storage, resume, and UI state behavior. The browser walkthrough
proves that the built binary renders and that two real Tailcat WebUI processes
can interoperate in this environment. One class of evidence is not presented as
a substitute for the other.

Windows filesystem runtime behavior is also separate. Local Linux tests cover
root replacement, symlinks, modeled reparse paths, hard links, permissions,
directory sync, and Windows cross-compilation. They cannot prove NTFS reparse,
DACL, link-count, or directory-sync semantics. CI now has a dedicated
`windows-latest` job with `CGO_ENABLED=0` that runs
`go test -count=1 ./internal/privatefs ./internal/transfer`. That remote result
remains pending until the branch is pushed and GitHub Actions runs it.

## Local automated result

- `make verify`, `go mod verify`, `go vet ./...`, fresh race tests, and fresh
  non-race tests pass with Go 1.27.0.
- `make verify` runs `./scripts/check-secrets_test.sh` and
  `./scripts/check-secrets.sh`. Scanner v1 rejects its synthetic GitHub-token
  fixture, prints only the fixture path, and reports `secret scan v1: ok` on
  the repository. This is a high-confidence pattern scan, not an
  entropy-complete credential audit.
- Vitest passes 10 test files and 76 tests. ESLint and the production Vite
  build pass.
- `pnpm peers check` reports no peer issues. The configured npm mirror has no
  advisory endpoint, so the exact default audit command reports a registry
  setup error. Repeating the high-severity audit against
  `registry.npmjs.org` reports no known vulnerabilities.
- Registry signature checking is supported by pnpm, but the lockfile's mirror
  packages expose no signing-key registry. The command audits zero packages
  and reports that no installed registry provides signing keys.
- Actionlint v1.7.7 accepts both workflows. Five explicit `CGO_ENABLED=0`
  builds produce nonempty Linux amd64/arm64, Windows amd64, and Darwin
  amd64/arm64 executables.
- The CI workflow contract test proves that `pnpm build` is followed by
  committed `webdist` parity before the embedded binary build. CI never copies
  newly built assets over the tracked tree.
- The final `govulncheck ./...` equivalent reports zero reachable
  vulnerabilities after the `x/crypto` update.
- This Linux host has no Docker, Podman, nerdctl, Buildah, Docker socket, or
  other compatible container engine. The required local `docker build` cannot
  start here. The unchanged Linux Docker build remains in CI and is a pending
  remote release check.

## Live two-peer result

Two isolated processes used separate temporary data roots and management
origins:

- Process A: `http://localhost:18080`
- Process B: `http://localhost:18081`

Process A created and started a Tailcat server. Process B created a
WebUI-managed client from A's standard connection token. The first topology
used public DERP and reported a 1,090 ms stock ping. After a required binary
restart changed A's ephemeral server identity, B's old token correctly became
unreachable. B deleted that client, used A's current token, and then reported a
direct path with a 0 ms stock ping.

WebUI diagnostics from B to A completed on reserved TCP `41640`:

- Ping: succeeded, direct, 0 ms.
- Duplex throughput: succeeded, direct, 1 MiB each direction, measured at
  126.4 MiB/s in this loopback environment.

The throughput number describes this one local run and is not a product
benchmark.

Secure transfer on reserved TCP `41641` also completed across the two
processes:

1. A selected a 45-byte browser file, finalized the share, rotated its
   one-time code, and dismissed the code before capture.
2. B created and started a receive job. It completed and its authenticated
   download matched the source SHA-256 and byte count.
3. A selected a 63,879,879-byte binary and finalized a second share.
4. B started and canceled the first attempt at zero bytes.
5. B retried, received all 63,879,879 bytes, and canceled while the final hash
   phase was still running.
6. A second retry reused staged blocks and completed. The downloaded byte count
   and SHA-256 matched the selected source.
7. B deleted the small completed job through the UI. The large completed job
   remains as screenshot evidence.

The initial CJK upload observation was a test-harness error. A relative fixture
path let the browser display the filename but not read its bytes. An absolute
path worked. A direct browser upload of `资料/文件.txt` with the shipped UTF-8
header returned HTTP 201, so no CJK product defect was recorded.

## Browser matrix

| Viewport | Locale and appearance | Surface | Result |
| --- | --- | --- | --- |
| 1440 × 900 | English, light | Diagnostics table with direct ping and duplex history | No overflow, no axe violations, no console/page errors |
| 768 × 900 | English, system light, reduced motion | Transfer table and receive form | Table breakpoint active, no overflow or sub-44 px targets, no axe violations |
| 390 × 844 | Simplified Chinese, dark | Transfer workflow and completed mobile history | List/card breakpoint active, 44 × 44 password control, no overflow, no axe violations |
| 320 × 800 | English, system dark, reduced motion | Diagnostics mobile list | No overflow or sub-44 px targets, no axe violations |

The 320 px keyboard pass focused the skip link first. Enter moved to
`#main-content`, and the next Tab focused Start diagnostic. At 768 px, reduced
motion removed transforms and retained only 120 ms color, background, border,
and opacity transitions. No native `alert`, `confirm`, or `prompt` dialog was
open in any final pass.

Axe reported zero violations in all four final matrix rows. Its remaining
`incomplete` items are automated contrast inference limits: the one-letter
avatar, and on some transfer views an overlapped Ant Select or selected menu
label. The avatar uses white on `#3A4749`, previously measured at 9.64:1.

The walkthrough also opened route empty states and all server settings tabs:
Port mappings, Allowed client keys, and Exit rules. Loading, validation, error,
cancel, retry, empty, completed, download, and deletion states were exercised
either live or by the deterministic UI suites.

## Resolved findings

1. A terminal diagnostic SSE event could arrive before the start response.
   The response then restored a stale running overlay until another render.
   The redundant response-side live seed was removed, with an ordering
   regression test.
2. Diagnostic success/path tags and compact measurement labels did not meet AA
   contrast in light mode. Stable diagnostic classes now use the verified
   light/dark semantic pairs; the final desktop axe scan has zero violations.
3. Ant Input.Password exposed a 14 × 14 visibility target on mobile. Its
   semantic role button is now 44 × 44, with a CSS regression guard and live
   geometry check.
4. Ant List.Item.Meta emitted an `h4` directly below the page `h1` on mobile.
   Diagnostic item content no longer introduces that skipped heading level.
5. The publish upgrade-close test asserted that a simulated remote goroutine
   had already received EOF. The service contract is local connection closure.
   The test now observes local Close synchronously and gives the simulated peer
   bounded time to run; 100 isolated repetitions pass.
6. Deferred release findings were closed: terminal diagnostic payload cleanup,
   strict valid-JSON event schema checks, exact transfer schema field
   allowlists, nonretryable resume timestamp cleanup, and a split that leaves
   `jobs.go` below 1,000 lines.
7. `govulncheck` found reachable GO-2026-6303 through Tailcat SSH and
   `golang.org/x/crypto` v0.54.0. The dependency is now v0.55.0, with its
   coupled `x/mod`, `x/text`, and `x/tools` updates. The fresh scan reports zero
   reachable vulnerabilities.

## Screenshots and privacy

- `docs/screenshots/diagnostics-desktop-light.png`: 1440 × 900 PNG.
- `docs/screenshots/transfers-mobile-dark-zh.png`: 390 × 844 PNG.

Both files came from the running embedded application and include the Tailcat
logo. Neither contains a connection token, one-time capability, private key,
cookie, host path, email address, or personal user data.

## Known environment and framework diagnostics

Vitest/jsdom prints `Not implemented: Window's getComputedStyle() method: with
pseudo-elements` while Ant mounts. The browser console is clean and Chromium
implements the API, so this remains a documented jsdom limitation rather than
a suppressed warning.

During cleanup, long-lived Tailcat or SSE work sometimes consumed the app's
graceful shutdown deadline. Process B exited after the timeout path. One A run
also returned `context deadline exceeded` after its active ephemeral server was
stopped. No staged-byte or SQLite mismatch was observed, but this environment
cleanup result is retained as a release concern rather than reported as a clean
shutdown.
