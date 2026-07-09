package state

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
)

func TestArmConfirmAndExpiry(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := filepath.Join(".ltk", "state.json")
	now := time.Unix(1_000_000, 0)

	s := Open(fs, path)
	if s.Armed("go test", now) {
		t.Fatal("nothing armed yet")
	}
	s.Arm("go test", now, 0, 30*time.Second)
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}

	// Survives a process boundary (reloaded from the same fs).
	s2 := Open(fs, path)
	if !s2.Armed("go test", now.Add(10*time.Second)) {
		t.Error("should be armed within the window")
	}
	if s2.Armed("go build", now.Add(10*time.Second)) {
		t.Error("only the exact command is armed")
	}
	if s2.Armed("go test", now.Add(31*time.Second)) {
		t.Error("should have expired after the window")
	}

	s2.Clear("go test")
	if s2.Armed("go test", now) {
		t.Error("cleared entry must not be armed")
	}
}

// With a delay, an entry is armed (live) but not Ready until the delay elapses,
// then Ready until the window closes.
func TestArmWithDelayBand(t *testing.T) {
	s := Open(afero.NewMemMapFs(), "state.json")
	now := time.Unix(1_000_000, 0)
	s.Arm("go test", now, 10*time.Second, 30*time.Second) // band [+10s, +30s]

	// Immediately: armed but too early to consume.
	if !s.Armed("go test", now) || s.Ready("go test", now) {
		t.Error("inside the delay: armed but not ready")
	}
	if got := s.RemainingDelay("go test", now); got != 10 {
		t.Errorf("RemainingDelay = %d, want 10", got)
	}
	// After the delay, before the window closes: ready.
	if !s.Ready("go test", now.Add(15*time.Second)) {
		t.Error("after the delay and within the window: should be ready")
	}
	if got := s.RemainingDelay("go test", now.Add(15*time.Second)); got != 0 {
		t.Errorf("RemainingDelay after delay = %d, want 0", got)
	}
	// After the window: neither armed nor ready.
	if s.Armed("go test", now.Add(31*time.Second)) || s.Ready("go test", now.Add(31*time.Second)) {
		t.Error("after the window: expired")
	}
}

func TestSavePrunesExpired(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := "state.json"
	now := time.Unix(2_000_000, 0)

	s := Open(fs, path)
	s.Arm("old", now, 0, 10*time.Second)
	if err := s.Save(now.Add(20 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if Open(fs, path).Armed("old", now.Add(20*time.Second)) {
		t.Error("expired entry should be pruned on save")
	}
}

// Save replaces an existing file in full and leaves no temp files behind (it
// writes to a sibling temp file and renames, so a concurrent reader never sees
// a half-written store).
func TestSaveReplacesAtomicallyWithoutLeftovers(t *testing.T) {
	fs := afero.NewMemMapFs()
	path := filepath.Join(".ltk", "state.json")
	now := time.Unix(3_000_000, 0)

	s := Open(fs, path)
	s.Arm("first", now, 0, 30*time.Second)
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}
	s.Arm("second", now, 0, 30*time.Second)
	if err := s.Save(now); err != nil {
		t.Fatal(err)
	}

	s2 := Open(fs, path)
	if !s2.Armed("first", now) || !s2.Armed("second", now) {
		t.Error("re-saved store must carry both entries")
	}
	infos, err := afero.ReadDir(fs, filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, fi := range infos {
		if fi.Name() != filepath.Base(path) {
			t.Errorf("unexpected leftover file %q after Save", fi.Name())
		}
	}
}

// Save surfaces write failures to the caller (who treats them as best-effort).
func TestSaveOnReadOnlyFsErrors(t *testing.T) {
	s := Open(afero.NewReadOnlyFs(afero.NewMemMapFs()), filepath.Join(".ltk", "state.json"))
	s.Arm("go test", time.Unix(1, 0), 0, 30*time.Second)
	if err := s.Save(time.Unix(1, 0)); err == nil {
		t.Error("Save on a read-only filesystem must error")
	}
}

func TestOpenMissingFileIsEmpty(t *testing.T) {
	s := Open(afero.NewMemMapFs(), "nope.json")
	if s.Armed("anything", time.Unix(1, 0)) {
		t.Error("missing file must yield an empty store")
	}
}
