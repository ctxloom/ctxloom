package ir_test

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// TestKnownShellsIsMutableByAnyImporter is the CHARACTERIZATION step for
// U072-F09. Shell.Valid is the sole validation gate for a user-supplied shell
// name (defaults.shell and every match.shells entry go through it) and it
// consults an exported package-level slice at call time, so any importer can
// silently redefine what "a known shell" means for the whole process.
//
// This is the wave-9 shape: the fix REMOVES the seam this test uses, so the
// test cannot be inverted into a red — it stops compiling. It is committed
// first so the defect is demonstrated on the real code rather than argued, and
// is replaced by the invariant pin when the fix lands.
func TestKnownShellsIsMutableByAnyImporter(t *testing.T) {
	saved := append([]ir.Shell(nil), ir.KnownShells...)
	t.Cleanup(func() { ir.KnownShells = saved })

	if !ir.ShellBash.Valid() {
		t.Fatal("fixture is not hostile: bash is not valid to begin with")
	}
	ir.KnownShells = []ir.Shell{ir.ShellCmd}
	if ir.ShellBash.Valid() {
		t.Error("an importer could not disable bash by reassigning KnownShells")
	}
	ir.KnownShells[0] = "anything-at-all"
	if !ir.Shell("anything-at-all").Valid() {
		t.Error("an importer could not admit an arbitrary shell by writing through KnownShells")
	}
}
