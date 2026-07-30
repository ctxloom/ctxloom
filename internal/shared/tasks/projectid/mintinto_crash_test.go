package projectid

import (
	"os"
	"testing"
)

// mintInto is a non-atomic two-step -- Mint appends and persists the registry
// entry, then WriteMarker stamps the tree -- and nothing can make the two
// atomic: they are separate files under separate roots (~/.ctxloom/projects
// vs <projectDir>/.ctxloom). So the intermediate state IS reachable: an id
// registered at a path whose tree carries no marker.
//
// What matters is that the state is not durable. The next resolution takes
// the registry-by-path fast path, sees the absent marker, and re-stamps it,
// so the tree recovers its own identity rather than minting a second one.
// That self-heal arm is the thing this pins; without it the markerless tree
// is exactly what oldTreeGone can later misread as "this tree moved".
func TestMintIntoCrashBetweenStepsSelfHeals(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()

	// The exact post-crash state: registry entry present, marker never written.
	e, err := m.Mint(dir)
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	if _, err := os.Stat(markerPathOf(t, dir)); !os.IsNotExist(err) {
		t.Fatalf("precondition: marker already present (stat err = %v)", err)
	}

	res := mustResolve(t, m, dir)
	if res.Action != ActionNormal {
		t.Fatalf("action = %q, want %q — the half-written mint must not fork or re-mint", res.Action, ActionNormal)
	}
	if res.ProjectID != e.ProjectID {
		t.Fatalf("id = %q, want the already-registered %q", res.ProjectID, e.ProjectID)
	}
	if got := markerOf(t, dir); got != e.ProjectID {
		t.Fatalf("marker = %q, want the registry's %q — the self-heal did not re-stamp the tree", got, e.ProjectID)
	}

	// And the registry did not grow a second entry for the same tree.
	entries, err := m.EntriesAtPath(dir)
	if err != nil {
		t.Fatalf("entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("registry holds %d entries for %s, want exactly 1", len(entries), dir)
	}
}
