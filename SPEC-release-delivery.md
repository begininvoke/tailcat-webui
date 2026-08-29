# Spec: release-delivery

## Objective

Make every commit verifiable and every version tag publish usable binaries and
containers, with documentation grounded in the actual running product.

## Tech Stack

GitHub Actions, pnpm 11, Node 26, Go 1.27, Docker Buildx, GHCR and GitHub
Releases.

## Commands

```sh
make verify
docker build -t tailcat-webui:verify .
```

## Project Structure

```text
.github/workflows/ci.yml       lint, test, build and Docker verification
.github/workflows/release.yml  tagged cross-platform binaries
.github/workflows/docker.yml   tagged amd64/arm64 GHCR image
docs/screenshots/              browser-captured desktop and mobile images
README.md / README_ZH.md       English and Chinese documentation
```

## Code Style

Actions use pinned major action versions, minimum permissions and immutable
frontend installs. Build metadata is injected through linker flags.

## Testing Strategy

CI runs frontend lint/test/build, Go module verification, vet, race tests,
CGO-disabled binary build and a single-platform Docker build. Release jobs use
a target matrix and upload checksummed archives.

## Boundaries

- Always: no plaintext secrets, least-privilege permissions, frozen lockfile,
  reproducible commands and generated screenshots from a running app.
- Ask first: publish to an external registry other than GHCR.
- Never: fabricate screenshots, use mutable local-only artifacts in README, or
  publish when verification fails.

## Success Criteria

- Tags build Linux amd64/arm64, Windows amd64 and macOS amd64/arm64 archives.
- Tags publish an amd64/arm64 OCI image to GHCR.
- README shows real 1440px desktop and 390px mobile screenshots.
