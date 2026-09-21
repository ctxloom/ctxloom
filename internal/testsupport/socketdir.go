package testsupport

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sunPathHeadroom is the longest unix socket path a test may bind. It sits
// below sun_path itself (108 bytes on Linux, 104 on macOS) so one number is
// portable, and it is the same budget the production socket pickers hold
// themselves to — grpc's pluginSocketDir and mcp's runnerSocketPath — so a
// fixture never binds somewhere production would have refused.
const sunPathHeadroom = 100

// SocketDir mints a fresh, user-private directory in which a unix socket
// named longestName can be bound, and removes it when the test ends.
// longestName is a path relative to the returned directory — pass the
// LONGEST one the test will bind there, since every shorter sibling then
// fits too.
//
// It deliberately does not use t.TempDir(). That roots at GOTMPDIR or
// TMPDIR, and inside a ctxloom agent cell both sit ~100 bytes deep under the
// session's ephemeral directory, so a socket bound beneath either overflows
// sun_path and the kernel refuses it with the opaque "bind: invalid
// argument" — an error that has been misread as a missing signing key. The
// cell keeps its long TMPDIR by decision, so the fixture is where the
// constraint belongs.
//
// The tiers are SocketTiers; the picking is PickSocketDir's, which fails the
// test naming every measured length rather than an errno.
func SocketDir(t testing.TB, longestName string) string {
	t.Helper()
	return socketDir(t, longestName, SocketTiers())
}

// SocketTiers is the preference order SocketDir tries, mirroring the
// production pickers' host tiers: the user's runtime dir, then os.TempDir()
// when it fits, then /tmp. The runtime-dir tier is a ctxloom-test SIBLING of
// production's $XDG_RUNTIME_DIR/ctxloom, never that directory itself: a test
// must not mint entries inside the live socket home that the runner's marker
// reaper walks.
func SocketTiers() []string {
	var tiers []string
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		tiers = append(tiers, filepath.Join(xdg, "ctxloom-test"))
	}
	return append(tiers, os.TempDir(), "/tmp")
}

// socketDir is SocketDir over an explicit tier list, so the fall-through and
// the loud failure can be exercised without a host whose /tmp is overlong.
func socketDir(t testing.TB, longestName string, tiers []string) string {
	t.Helper()
	dir, remove, err := PickSocketDir(longestName, tiers)
	if err != nil {
		t.Fatalf("testsupport.SocketDir: %v", err)
		return ""
	}
	t.Cleanup(remove)
	return dir
}

// PickSocketDir is the picker under SocketDir for a fixture that has no
// testing.TB — an acceptance step that returns errors. It mints a fresh
// directory under the first tier in which longestName fits the sun_path
// budget and returns it with the func that removes it. A tier is taken only
// once its minted path has been MEASURED to fit. When none does, the error
// names every measured length and the budget, so the next occurrence
// self-diagnoses instead of surfacing as an errno.
func PickSocketDir(longestName string, tiers []string) (dir string, remove func(), err error) {
	var tried []string
	for _, tier := range tiers {
		if err := os.MkdirAll(tier, 0o700); err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", tier, err))
			continue
		}
		dir, err := os.MkdirTemp(tier, "sock")
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s: %v", tier, err))
			continue
		}
		path := filepath.Join(dir, longestName)
		if len(path) <= sunPathHeadroom {
			return dir, func() { _ = os.RemoveAll(dir) }, nil
		}
		_ = os.RemoveAll(dir)
		tried = append(tried, fmt.Sprintf("%s: %d bytes", path, len(path)))
	}
	return "", nil, fmt.Errorf("no directory short enough to bind a unix socket named %q within the %d-byte sun_path budget; measured:\n  %s",
		longestName, sunPathHeadroom, strings.Join(tried, "\n  "))
}
