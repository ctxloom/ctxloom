package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCommands_RunRelaysTheLaunchersExitStatus: a gomutants that fails is
// reported with its own exit status, not a generic 1, so the recipe (and CI)
// sees what gomutants said.
func TestCommands_RunRelaysTheLaunchersExitStatus(t *testing.T) {
	cmdModule(t)
	code, out := mutshard("run", "-scope", "tree", "-out", t.TempDir(), "--", "sh", "-c", "exit 3")
	if code != 3 || !strings.Contains(out, "gomutants") {
		t.Errorf("exit %d (want 3), output must name gomutants:\n%s", code, out)
	}
}

// TestTallyCount_AnUnknownStatusIsUnreadable: a status this tool does not
// know is a report it cannot vouch for, never a mutant silently left out of
// every count.
func TestTallyCount_AnUnknownStatusIsUnreadable(t *testing.T) {
	var tl tally
	err := tl.count(0, gomutantsMutation{ID: "internal/a/f.go:F:T#1", Status: "SKIPPED", Line: 3})
	if !errors.Is(err, errUnreadable) {
		t.Errorf("err = %v, want errUnreadable", err)
	}
	if tl.total() != 0 {
		t.Errorf("an unreadable mutant was counted: %+v", tl)
	}
}

// TestRatchet_AMissingBaselineIsItsOwnError: a baseline file that is not
// there is that, not an unbaselined package.
func TestRatchet_AMissingBaselineIsItsOwnError(t *testing.T) {
	err := ratchet(&bytes.Buffer{}, filepath.Join(t.TempDir(), "absent.txt"), "internal/a", survivors(1, 1), false)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %v, want fs.ErrNotExist", err)
	}
	if errors.Is(err, errUnbaselined) {
		t.Errorf("a missing file was reported as an unbaselined package: %v", err)
	}
}

// TestReadBaseline_ADuplicateNamesItsTarget: the refusal of a duplicated row
// names the target that is duplicated.
func TestReadBaseline_ADuplicateNamesItsTarget(t *testing.T) {
	f := filepath.Join(t.TempDir(), "b.txt")
	if err := os.WriteFile(f, []byte("internal/dup 1 0 measured\ninternal/dup 2 0 measured\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readBaseline(f)
	if err == nil || !strings.Contains(err.Error(), "internal/dup has more than one row") {
		t.Errorf("err = %v, want it to name internal/dup", err)
	}
}
