//go:build acceptance

package acceptance

import (
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
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

// A container's mount destinations show in `docker diff` only as the stub
// directories the runtime made to mount onto (writes into a bind mount never
// reach the writable layer), and `--init` puts the runtime's init binary in
// the image's sbin. None of it is a write the engine made. This is the shape
// a live claude-code container-rootless cell produced: the instance home,
// the trace dir and the secret mount among its mounts.
func TestProbeContainerUnexpected_MountStubsAndRuntimeInitAreNotWrites(t *testing.T) {
	diff := []string{
		"A /ctxloom-probe-trace",
		"A /tmp/ctxloom-integration-1/project",
		"A /ctxloom", "A /ctxloom/home", "A /ctxloom/home/claude",
		"C /home", "C /home/ctxloom", "A /home/ctxloom/.claude",
		"C /usr", "C /usr/sbin", "A /usr/sbin/docker-init",
		"C /run", "A /run/ctxloom", "A /run/ctxloom/secrets",
	}
	mounts := []string{"/ctxloom/home/claude", "/ctxloom-probe-trace", "/tmp/ctxloom-integration-1/project", "/run/ctxloom/secrets"}

	if got := probeContainerUnexpected(diff, "/home/ctxloom", mounts); len(got) != 0 {
		t.Fatalf("mount stubs and the runtime's init were reported as engine writes: %v", got)
	}
}

// A mount exempts its own stub and the directories above it, never a sibling
// or a path that merely shares its prefix: a write there IS a leak.
func TestProbeContainerUnexpected_WritesBesideAMountAreStillLeaks(t *testing.T) {
	diff := []string{
		"A /ctxloom/home/claude",
		"A /ctxloom/leak.txt",
		"A /ctxloom/home/claude-other",
		"A /usr/sbin/other",
		"A /home/someone/x",
	}
	got := probeContainerUnexpected(diff, "/home/ctxloom", []string{"/ctxloom/home/claude"})
	want := []string{"A /ctxloom/leak.txt", "A /ctxloom/home/claude-other", "A /usr/sbin/other", "A /home/someone/x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("unexpected writes = %v, want %v", got, want)
	}
}
