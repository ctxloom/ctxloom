package projectid

import (
	"os"
	"path/filepath"
	"testing"
)

// TestResolutionActionWarningContract pins the pairing between Resolution's
// two outcome fields: Warning is non-empty for exactly the outcomes that need
// a human told (Moved, Forked) and empty otherwise, so Action is the ONLY
// thing that distinguishes "resolved in place" from "minted a fresh identity"
// — the two outcomes that share an empty Warning. A caller reading Warning
// alone cannot tell those apart, which is why the taxonomy is a contract and
// not test scaffolding.
func TestResolutionActionWarningContract(t *testing.T) {
	regPath := filepath.Join(t.TempDir(), "index.yaml")
	m, err := Open(regPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	dir := t.TempDir()
	fresh, err := m.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve(new): %v", err)
	}
	if fresh.Action != ActionNewProject || fresh.Warning != "" {
		t.Fatalf("first resolve = {%q, %q}, want {%q, \"\"}", fresh.Action, fresh.Warning, ActionNewProject)
	}

	again, err := m.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve(same): %v", err)
	}
	if again.Action != ActionNormal || again.Warning != "" {
		t.Fatalf("second resolve = {%q, %q}, want {%q, \"\"}", again.Action, again.Warning, ActionNormal)
	}
	if again.ProjectID != fresh.ProjectID {
		t.Fatalf("identity changed across resolves: %q then %q", fresh.ProjectID, again.ProjectID)
	}

	// A moved tree: the marker travels, the registered path does not.
	moved := filepath.Join(t.TempDir(), "moved")
	if err := os.MkdirAll(filepath.Join(moved, ".ctxloom"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(moved, ".ctxloom", "project-id"), []byte(fresh.ProjectID), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("remove original: %v", err)
	}
	res, err := m.Resolve(moved)
	if err != nil {
		t.Fatalf("Resolve(moved): %v", err)
	}
	if res.Action != ActionMoved {
		t.Fatalf("moved resolve action = %q, want %q", res.Action, ActionMoved)
	}
	if res.Warning == "" {
		t.Error("a Moved outcome carried no Warning — nothing would tell the user their store followed the tree")
	}
}
