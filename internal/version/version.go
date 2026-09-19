// Package version holds the build-stamped version string as a leaf with no
// ctxloom imports. It was split out of internal/adapters/cli (which owned it as
// cli.Version) so that packages internal/adapters/cli itself imports — starting with
// the MCP surface being pulled out of internal/adapters/cli — can still read the
// stamp without creating an import cycle back into internal/adapters/cli.
package version

// Version is the build stamp, set at build time via ldflags:
//
//	-X github.com/ctxloom/ctxloom/internal/version.Version=v0.7.0-27a90cd-20260826T125736
//
// There is deliberately NO default. An unstamped binary cannot say which build
// or commit answered, and this project's whole verification rule rests on
// being able to say that: a stale binary plus an exit-code check agrees with
// anything. A default that PROCEEDS under an unidentifiable identity is what
// lets confident wrong work through — an agent image reused under a key that
// fits any build, a version comparison that silently declines to compare.
//
// The empty zero value is therefore a REFUSAL, not a mode. ValidStamp is the
// single authority on what counts as stamped; internal/adapters/cli's root gate turns a
// failing answer into a startup abort naming the remedy.
var Version string
