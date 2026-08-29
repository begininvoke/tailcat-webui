package webdist

import "embed"

// Files contains the production web bundle. The placeholder is replaced by
// `make web-build` before release builds.
//
//go:embed dist
var Files embed.FS
