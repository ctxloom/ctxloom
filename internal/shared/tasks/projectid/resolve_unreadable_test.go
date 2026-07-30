package projectid

import (
	"os"
	"path/filepath"
	"testing"
)

// cleanPath silently swaps its comparison key when EvalSymlinks fails
// (resolved path -> lexical Clean), so a tree registered under its RESOLVED
// path and later queried through a symlink whose resolution now fails does
// compare unequal to itself. What must never follow from that skew is a
// silent fork: minting a fresh identity into a tree that already has one
// overwrites its marker and orphans its task log, which is the disaster
// cleanPath's own doc records.
//
// The skew is only constructible by denying access to a path component, and
// that denial reaches the marker read too, so Resolve fails loud instead.
// Measured on Linux: every permission/mount failure that breaks
// filepath.EvalSymlinks breaks os.ReadFile on the same path. (The one
// measured asymmetry runs the other way -- a 45-deep symlink chain resolves
// fine under EvalSymlinks and is ELOOP to the kernel.)
func TestResolveFailsLoudWhenPathResolutionIsDenied(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory mode bits do not deny traversal")
	}
	base := t.TempDir()
	mid := filepath.Join(base, "mid")
	proj := filepath.Join(mid, "proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(proj, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	m := newManager(t)
	first := mustResolve(t, m, link) // registers the RESOLVED path, proj

	if err := os.Chmod(mid, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(mid, 0o755) })

	// Precondition: the skew the row describes really is present here.
	if cleanPath(link) == cleanPath(proj) {
		t.Fatalf("expected cleanPath skew: %q vs %q", cleanPath(link), cleanPath(proj))
	}

	res, err := m.Resolve(link)
	if err == nil {
		t.Fatalf("Resolve on an unreadable tree returned %+v, nil — want a loud failure", res)
	}
	if res.ProjectID != "" {
		t.Fatalf("Resolve returned id %q alongside its error", res.ProjectID)
	}

	// And the registered identity is untouched: nothing forked, nothing
	// re-pointed, the marker still names the original project.
	_ = os.Chmod(mid, 0o755)
	if got := markerOf(t, proj); got != first.ProjectID {
		t.Fatalf("marker = %q, want the original %q — a fork overwrote it", got, first.ProjectID)
	}
	e, _ := m.ResolveByID(first.ProjectID)
	if e == nil || cleanPath(e.Path) != cleanPath(proj) {
		t.Fatalf("registry entry for %s disturbed", first.ProjectID)
	}
}
