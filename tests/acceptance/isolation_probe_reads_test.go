//go:build acceptance

package acceptance

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/lm/isolation"
)

// baseContainerPass is a probeResult that satisfies container guarantees (a)–(d)
// so a test can isolate the new (e) read-observation check.
func baseContainerPass() *probeResult {
	return &probeResult{
		Engine:        "claude-code",
		Axis:          probeAxisContainerRootless,
		ExitCode:      0,
		ContainerHome: "/home/ctxloom",
		Container:     probeContainerSnapshot{Name: "ctxloom-iso-abc", Diff: []string{"C /home/ctxloom"}},
	}
}

// TestAssertProbeContainer_RequiresReads: a container cell that captured NO
// reads must FAIL (e) — that is the write-only-fallback the strace instrument
// exists to prevent (strace missing, SYS_PTRACE not granted, trace lost).
func TestAssertProbeContainer_RequiresReads(t *testing.T) {
	res := baseContainerPass()
	res.ReadsErr = "trace file absent"
	// res.Reads left empty
	err := assertProbeContainer(res)
	if err == nil || !strings.Contains(err.Error(), "(e) read observation") {
		t.Fatalf("empty read-set must fail the (e) read-observation guarantee; got %v", err)
	}
}

// TestAssertProbeContainer_PassesWithReads: guarantees (a)–(e) all hold when the
// probe observed at least one real read.
func TestAssertProbeContainer_PassesWithReads(t *testing.T) {
	res := baseContainerPass()
	res.Reads = []isolation.TraceRead{
		{Path: "/home/ctxloom/project/CLAUDE.md", Syscall: "openat", Result: "ok"},
		{Path: "/home/ctxloom/.claude/settings.json", Syscall: "access", Result: "ENOENT"},
	}
	if err := assertProbeContainer(res); err != nil {
		t.Fatalf("a container cell with a real read-set must pass; got %v", err)
	}
	if !probeReadsHasFailedResult(res.Reads) {
		t.Error("expected the ENOENT row to be detected as a failed (probed-not-found) read")
	}
}
