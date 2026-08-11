// Package version holds the build-stamped version string as a leaf with no
// ctxloom imports. It was split out of internal/cli (which owned it as
// cli.Version) so that packages internal/cli itself imports — starting with
// the MCP surface being pulled out of internal/cli — can still read the
// stamp without creating an import cycle back into internal/cli.
package version

// Version is set at build time via ldflags.
// Example: go build -ldflags "-X github.com/ctxloom/ctxloom/internal/version.Version=v1.0.0"
var Version = "dev"
