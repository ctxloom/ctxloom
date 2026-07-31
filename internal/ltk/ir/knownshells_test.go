package ir_test

import (
	"slices"
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// TestKnownShellsCannotBeMutatedByImporters is the inverted form of the
// characterization committed for U072-F09. Shell.Valid is the sole validation
// gate for a user-supplied shell name (defaults.shell and every match.shells
// entry go through it), so the set it consults must not be reachable from
// outside the package. The pre-fix test — which reassigned ir.KnownShells and
// watched bash stop being valid — cannot be turned into a red because the fix
// deletes the seam it used; it stops compiling, which is not a red. This pin
// is what survives.
func TestKnownShellsCannotBeMutatedByImporters(t *testing.T) {
	got := ir.KnownShells()
	if !slices.Contains(got, ir.ShellBash) {
		t.Fatalf("fixture is not hostile: KnownShells() = %v, expected bash among them", got)
	}

	// Writing through the returned slice must not change what Valid accepts,
	// and must not be visible to the next caller.
	for i := range got {
		got[i] = "clobbered"
	}
	if !ir.ShellBash.Valid() {
		t.Error("clobbering the returned slice changed Valid: KnownShells is not returning a copy")
	}
	if ir.Shell("clobbered").Valid() {
		t.Error("a value written into the returned slice became a valid shell")
	}
	if again := ir.KnownShells(); slices.Contains(again, "clobbered") {
		t.Errorf("KnownShells() = %v after clobbering: it must return a fresh slice each call", again)
	}
}

// TestValidAcceptsExactlyTheKnownSet keeps Valid and KnownShells from drifting
// apart now that they read the same unexported list through different doors.
func TestValidAcceptsExactlyTheKnownSet(t *testing.T) {
	for _, s := range ir.KnownShells() {
		if !s.Valid() {
			t.Errorf("KnownShells() lists %q but Valid() rejects it", s)
		}
	}
	for _, s := range []ir.Shell{"", "fish", "nu", "Bash", "sh "} {
		if s.Valid() {
			t.Errorf("Valid() accepted %q, which is not in KnownShells()", s)
		}
	}
}
