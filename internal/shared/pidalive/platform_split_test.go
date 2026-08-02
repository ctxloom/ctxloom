package pidalive

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlatformProbes_AreGenuinelyDivergent guards against a stale claim that
// a build-tagged pidAlive pair was byte-identical apart from the //go:build
// line, both "delegating to pidalive.Alive — a package that already owns the
// platform split", concluding the build tags bought nothing. No such pair
// exists any more: that consolidation is what CREATED this package (see the
// package doc — three near-identical copies in agentcoord/coord,
// internal/operations and internal/lm/isolation were folded into one leaf
// package), `pidAlive` survives in the repo only inside comments and one
// test-name mention, and `Alive` is a State CONSTANT, not a function anything
// can delegate to.
//
// The two files here are the consolidated package's OWN platform
// implementations, and their divergence is irreducible: Unix has a signal-0
// probe (kill(2)) and Windows has none, so Windows must interpret
// OpenProcess's errnos instead — including treating ERROR_ACCESS_DENIED as
// ALIVE, a live process this token merely cannot open. Collapsing them into
// one implementation would reintroduce that defect.
//
// Pinning the divergence structurally: this goes red if either implementation
// is rewritten in terms of the other, or if the build-tagged split is removed.
func TestPlatformProbes_AreGenuinelyDivergent(t *testing.T) {
	unix := readSource(t, "pidalive_unix.go")
	windows := readSource(t, "pidalive_windows.go")

	assert.Contains(t, unix, "//go:build !windows")
	assert.Contains(t, windows, "//go:build windows")

	assert.Contains(t, unix, "syscall.Signal(0)",
		"the Unix probe is a signal-0 probe; there is no portable equivalent to share")
	assert.NotContains(t, unix, "OpenProcess")

	assert.Contains(t, windows, "ERROR_ACCESS_DENIED",
		"the Windows probe must read OpenProcess errnos, and must map access-denied to ALIVE")
	assert.NotContains(t, windows, "syscall.Signal(0)")

	assert.NotEqual(t, stripBuildConstraint(unix), stripBuildConstraint(windows),
		"the two implementations are not the same code behind different tags")
}

// TestWindowsProbe_ReleasesTheProcessHandle pins the Windows arm's resource
// discipline from a host that cannot execute it. On Windows os.FindProcess IS
// an OpenProcess call, so every successful probe acquires a kernel handle; the
// probe's heaviest caller polls on a timer, so discarding the *os.Process and
// waiting for a finalizer is a steady leak. Nothing on Linux can run this code
// path, so the guard is structural: the returned process must be bound and
// released, never discarded into `_`.
func TestWindowsProbe_ReleasesTheProcessHandle(t *testing.T) {
	windows := readSource(t, "pidalive_windows.go")

	assert.NotContains(t, windows, "_, err := os.FindProcess(pid)",
		"the process must not be discarded: on Windows that handle stays open until the finalizer runs")
	assert.Contains(t, windows, "p, err := os.FindProcess(pid)")
	assert.Contains(t, windows, "p.Release()",
		"the handle OpenProcess returned must be released once the verdict is known")
}

// readSource reads one of this package's own source files. Tests run with the
// package directory as their working directory, so the bare name resolves.
func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	require.NoError(t, err)
	return string(b)
}

// stripBuildConstraint drops the leading //go:build line so the comparison is
// of the implementations themselves, exactly the equivalence that mattered.
func stripBuildConstraint(src string) string {
	var kept []string
	for _, line := range strings.Split(src, "\n") {
		if strings.HasPrefix(line, "//go:build") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}
