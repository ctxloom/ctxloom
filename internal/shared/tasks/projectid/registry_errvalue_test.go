package projectid

import (
	"os"
	"path/filepath"
	"testing"
)

// unwritableRegistry seeds a registry file containing seed, pre-creates its
// lock sidecar, then makes the containing directory unwritable — so the
// lock file still opens (the sidecar already exists) and loadLocked still
// reads, but saveLocked's atomic write cannot create its temp file. That is
// the only shape that reaches a mutator's persist-failure path without
// failing earlier.
func unwritableRegistry(t *testing.T, seed string) *Manager {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory mode bits do not deny writes")
	}
	dir := filepath.Join(t.TempDir(), "projects")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "index.yaml")
	if err := os.WriteFile(path, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed registry: %v", err)
	}
	if err := os.WriteFile(path+".lock", nil, 0o644); err != nil {
		t.Fatalf("seed lock: %v", err)
	}
	m, err := Open(path)
	if err != nil {
		t.Fatalf("open registry: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	return m
}

// Every registry mutator must zero its Entry when it returns an error. Adopt
// once had the one return in the file that did not: its already-registered-id
// arm read `return out, m.saveLocked(reg)`, handing back a fully populated
// Entry alongside a possibly non-nil error, so a caller that checked the
// value before the error saw a plausible identity that was never persisted.
// The mutate() seam that every mutator now shares is what makes the zeroing
// uniform; this pins that a persist failure never leaks a value again.
func TestMutatorsReturnZeroEntryOnPersistFailure(t *testing.T) {
	const seeded = "projects:\n    - project_id: seeded-id\n      path: /nowhere/seeded\n"

	t.Run("adopt existing id", func(t *testing.T) {
		m := unwritableRegistry(t, seeded)
		e, err := m.Adopt("seeded-id", t.TempDir())
		if err == nil {
			t.Fatal("Adopt into an unwritable registry succeeded; expected a persist failure")
		}
		if e != (Entry{}) {
			t.Fatalf("Adopt returned %+v alongside error %v — want the zero Entry", e, err)
		}
	})

	t.Run("adopt new id", func(t *testing.T) {
		m := unwritableRegistry(t, seeded)
		e, err := m.Adopt("brand-new-id", t.TempDir())
		if err == nil {
			t.Fatal("Adopt into an unwritable registry succeeded; expected a persist failure")
		}
		if e != (Entry{}) {
			t.Fatalf("Adopt returned %+v alongside error %v — want the zero Entry", e, err)
		}
	})

	t.Run("mint", func(t *testing.T) {
		m := unwritableRegistry(t, seeded)
		e, err := m.Mint(t.TempDir())
		if err == nil {
			t.Fatal("Mint into an unwritable registry succeeded; expected a persist failure")
		}
		if e != (Entry{}) {
			t.Fatalf("Mint returned %+v alongside error %v — want the zero Entry", e, err)
		}
	})
}
