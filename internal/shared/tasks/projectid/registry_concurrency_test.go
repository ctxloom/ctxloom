package projectid

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// TestMutators_NoLostUpdatesUnderConcurrency pins the invariant the registry's
// lock order exists for: every concurrent Mint and Adopt survives in the
// persisted registry. A mutator that read outside the file lock would lose
// whichever write landed between its own load and save, and would do so only
// under concurrency — silently, on a single-process test run.
func TestMutators_NoLostUpdatesUnderConcurrency(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "index.yaml"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	const n = 12
	var wg sync.WaitGroup
	minted := make([]string, n)
	adopted := make([]string, n)
	for i := range n {
		wg.Add(2)
		go func() {
			defer wg.Done()
			e, err := m.Mint(filepath.Join(t.TempDir(), fmt.Sprintf("mint-%d", i)))
			if err != nil {
				t.Errorf("Mint(%d): %v", i, err)
				return
			}
			minted[i] = e.ProjectID
		}()
		go func() {
			defer wg.Done()
			id := fmt.Sprintf("adopted-id-%d", i)
			if _, err := m.Adopt(id, filepath.Join(t.TempDir(), fmt.Sprintf("adopt-%d", i))); err != nil {
				t.Errorf("Adopt(%d): %v", i, err)
				return
			}
			adopted[i] = id
		}()
	}
	wg.Wait()

	reg, err := m.load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	present := make(map[string]struct{}, len(reg.Projects))
	for _, e := range reg.Projects {
		present[e.ProjectID] = struct{}{}
	}
	for _, id := range append(append([]string{}, minted...), adopted...) {
		if id == "" {
			continue
		}
		if _, ok := present[id]; !ok {
			t.Errorf("project-id %q was written but is absent from the persisted registry (lost update)", id)
		}
	}
	if len(present) != 2*n {
		t.Errorf("registry holds %d distinct ids, want %d", len(present), 2*n)
	}

	// Repoint must land through the same seam.
	if minted[0] != "" {
		want := filepath.Join(t.TempDir(), "repointed")
		if err := m.Repoint(minted[0], want); err != nil {
			t.Fatalf("Repoint: %v", err)
		}
		e, err := m.ResolveByID(minted[0])
		if err != nil || e == nil {
			t.Fatalf("ResolveByID after Repoint = %v, %v", e, err)
		}
		if e.Path != cleanPath(want) {
			t.Errorf("Repoint left path %q, want %q", e.Path, cleanPath(want))
		}
	}
}
