package projectid

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/paths"
)

// newManager returns a Manager backed by a fresh temp registry file.
func newManager(t *testing.T) *Manager {
	t.Helper()
	reg := filepath.Join(t.TempDir(), "projects", "index.yaml")
	m, err := Open(reg)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	return m
}

func mustResolve(t *testing.T, m *Manager, dir string) Resolution {
	t.Helper()
	res, err := m.Resolve(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	return res
}

func markerOf(t *testing.T, dir string) string {
	t.Helper()
	got, err := ReadMarker(dir)
	if err != nil {
		t.Fatalf("read marker %s: %v", dir, err)
	}
	return got
}

func TestResolveNewProject(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	res := mustResolve(t, m, dir)
	if res.Action != ActionNewProject {
		t.Fatalf("action = %q, want %q", res.Action, ActionNewProject)
	}
	if res.ProjectID == "" {
		t.Fatal("empty project id")
	}
	if got := markerOf(t, dir); got != res.ProjectID {
		t.Fatalf("marker = %q, want %q", got, res.ProjectID)
	}
	if e, _ := m.ResolveByPath(dir); e == nil || e.ProjectID != res.ProjectID {
		t.Fatalf("registry missing entry for %s", dir)
	}
}

func TestResolveNormalInPlace(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	first := mustResolve(t, m, dir)
	second := mustResolve(t, m, dir)
	if second.Action != ActionNormal {
		t.Fatalf("action = %q, want %q", second.Action, ActionNormal)
	}
	if second.ProjectID != first.ProjectID {
		t.Fatalf("id changed: %q -> %q", first.ProjectID, second.ProjectID)
	}
}

func TestResolveMoved(t *testing.T) {
	m := newManager(t)
	oldDir := t.TempDir()
	first := mustResolve(t, m, oldDir)

	// Simulate `mv oldDir newDir`: the marker travels, the old tree is gone.
	newDir := t.TempDir()
	if err := WriteMarker(newDir, first.ProjectID); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.RemoveAll(oldDir); err != nil {
		t.Fatalf("rm oldDir: %v", err)
	}

	res := mustResolve(t, m, newDir)
	if res.Action != ActionMoved {
		t.Fatalf("action = %q, want %q", res.Action, ActionMoved)
	}
	if res.ProjectID != first.ProjectID {
		t.Fatalf("id changed on move: %q -> %q", first.ProjectID, res.ProjectID)
	}
	e, _ := m.ResolveByID(first.ProjectID)
	if e == nil || cleanPath(e.Path) != cleanPath(newDir) {
		t.Fatalf("registry not re-pointed to %s", newDir)
	}
}

func TestResolveForkedCopy(t *testing.T) {
	m := newManager(t)
	oldDir := t.TempDir()
	first := mustResolve(t, m, oldDir)

	// Simulate `cp -r oldDir newDir`: same marker, oldDir still live.
	newDir := t.TempDir()
	if err := WriteMarker(newDir, first.ProjectID); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res := mustResolve(t, m, newDir)
	if res.Action != ActionForked {
		t.Fatalf("action = %q, want %q", res.Action, ActionForked)
	}
	if res.ProjectID == first.ProjectID {
		t.Fatal("fork reused the original id")
	}
	if got := markerOf(t, newDir); got != res.ProjectID {
		t.Fatalf("copy marker = %q, want rewritten %q", got, res.ProjectID)
	}
	// Original untouched.
	if e, _ := m.ResolveByID(first.ProjectID); e == nil || cleanPath(e.Path) != cleanPath(oldDir) {
		t.Fatalf("original entry disturbed")
	}
}

func TestResolveForkedInconclusive(t *testing.T) {
	m := newManager(t)
	oldDir := t.TempDir()

	// Register an id at oldDir without a readable marker: make the marker path
	// a directory so ReadMarker errors (an inconclusive probe).
	const ghost = "ghost-id"
	if _, err := m.Adopt(ghost, oldDir); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	if err := os.MkdirAll(paths.ProjectMarkerPath(oldDir), 0o755); err != nil {
		t.Fatalf("mk marker dir: %v", err)
	}

	newDir := t.TempDir()
	if err := WriteMarker(newDir, ghost); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res := mustResolve(t, m, newDir)
	if res.Action != ActionForked {
		t.Fatalf("action = %q, want %q", res.Action, ActionForked)
	}
	if res.ProjectID == ghost {
		t.Fatal("inconclusive probe reused the contested id")
	}
}

func TestResolveAdoptUnknownMarker(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	// A marker naming an id the registry has never seen (fresh machine).
	const known = "lonely-id"
	if err := WriteMarker(dir, known); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	res := mustResolve(t, m, dir)
	if res.Action != ActionNormal {
		t.Fatalf("action = %q, want %q", res.Action, ActionNormal)
	}
	if res.ProjectID != known {
		t.Fatalf("id = %q, want adopted %q", res.ProjectID, known)
	}
	if e, _ := m.ResolveByPath(dir); e == nil || e.ProjectID != known {
		t.Fatalf("registry did not adopt id at %s", dir)
	}
}

// TestResolveRejectsCraftedMarker pins the traversal guard: a marker committed
// by a third party (the file is gitignored only by convention) must never be
// adopted as identity, since the id becomes a task-log filename. Resolve fails
// closed rather than minting/adopting a traversal-shaped id.
func TestResolveRejectsCraftedMarker(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	// Write the marker file directly — an attacker doesn't go through
	// WriteMarker — with a path-traversal payload.
	markerPath := paths.ProjectMarkerPath(dir)
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("../../../escape\n"), 0o644); err != nil {
		t.Fatalf("write crafted marker: %v", err)
	}

	if _, err := m.Resolve(dir); err == nil {
		t.Fatal("Resolve adopted a traversal marker; want rejection")
	}
	if _, err := ReadMarker(dir); err == nil {
		t.Fatal("ReadMarker returned a traversal id; want rejection")
	}
}

func TestMintUniqueAgainstRegistry(t *testing.T) {
	m := newManager(t)
	seen := map[string]struct{}{}
	for range 50 {
		e, err := m.Mint(t.TempDir())
		if err != nil {
			t.Fatalf("mint: %v", err)
		}
		if _, dup := seen[e.ProjectID]; dup {
			t.Fatalf("duplicate project id minted: %q", e.ProjectID)
		}
		seen[e.ProjectID] = struct{}{}
	}
}

// TestResolveSymlinkedPathKeepsIdentity pins path canonicalization: the same
// tree reached through a symlink (or an alternate mount like /tmp vs
// /private/tmp) is the SAME project. Without symlink resolution the symlinked
// launch missed its registry entry, concluded "live copy", forked a fresh
// identity, and overwrote the in-tree marker — orphaning the project's task
// log.
func TestResolveSymlinkedPathKeepsIdentity(t *testing.T) {
	m := newManager(t)
	real := t.TempDir()

	first := mustResolve(t, m, real)
	if first.Action != ActionNewProject {
		t.Fatalf("setup: action = %q, want %q", first.Action, ActionNewProject)
	}

	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "via-link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	second := mustResolve(t, m, link)
	if second.ProjectID != first.ProjectID {
		t.Fatalf("symlinked launch forked identity: %q != %q", second.ProjectID, first.ProjectID)
	}
	if second.Action != ActionNormal {
		t.Fatalf("action = %q, want %q (no fork, no move)", second.Action, ActionNormal)
	}
	if got := markerOf(t, real); got != first.ProjectID {
		t.Fatalf("marker was rewritten to %q, want original %q", got, first.ProjectID)
	}
}

// TestEntriesAtPath_ReturnsEveryEntryAtThatPath is the regression test for
// the registry primitive the silent-empty-backlog fix depends on (see
// tasks/operations.missingLogSiblingNote): unlike ResolveByPath's
// first-match fast path, EntriesAtPath must surface EVERY entry bound to a
// path, including a collision left behind by two ids both getting
// registered at the same tree (e.g. two Adopt calls for different unknown
// marker ids over time).
func TestEntriesAtPath_ReturnsEveryEntryAtThatPath(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	if _, err := m.Adopt("old-id", dir); err != nil {
		t.Fatalf("adopt old-id: %v", err)
	}
	if _, err := m.Adopt("new-id", dir); err != nil {
		t.Fatalf("adopt new-id: %v", err)
	}

	entries, err := m.EntriesAtPath(dir)
	if err != nil {
		t.Fatalf("entries at path: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2 (both old-id and new-id bound to %s)", entries, dir)
	}
	ids := map[string]bool{}
	for _, e := range entries {
		ids[e.ProjectID] = true
	}
	if !ids["old-id"] || !ids["new-id"] {
		t.Fatalf("entries = %+v, want both old-id and new-id", entries)
	}
}

// TestEntriesAtPath_EmptyForAnUnregisteredPath is the common case: a path
// nothing has ever registered returns zero entries, not an error.
func TestEntriesAtPath_EmptyForAnUnregisteredPath(t *testing.T) {
	m := newManager(t)
	entries, err := m.EntriesAtPath(t.TempDir())
	if err != nil {
		t.Fatalf("entries at path: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entries = %+v, want none", entries)
	}
}
