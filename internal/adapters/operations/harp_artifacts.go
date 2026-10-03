package operations

import (
	"fmt"
	"os"
	"sort"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// HarpTopLevelArtifacts returns the base names — sorted — of the regular
// files at a harp directory's TOP LEVEL that no paths.HarpMembers row names:
// design notes, audits, write-ups, and above all the *.plan.md documents a
// session is told to write, landed in the machine session dir instead of the
// session's output dir.
//
// WHY THE TOP LEVEL IS THE WRONG PLACE. The session dir holds machine state
// only, and a containerized run reaches only its Mounted rows
// (paths.MountedMembers) — so an authored file at the top level, which no row
// classifies, is written into container-ephemeral overlay space and is gone
// when the container exits; on the host it sits where no human looks. The
// write returns nil, the file is readable for the length of the run, and on a
// container zero bytes remain afterwards. cli.doctorCheckHarpDurability
// reports that population through this predicate; nothing moves it.
//
// Only REGULAR files are named. Directories are members or nothing this
// check is about; a socket, FIFO or symlink is not a design note anybody can
// lose, and Type() comes from lstat so a link is non-regular whatever it
// points at.
//
// A missing directory yields no names and no error: a harp that has authored
// nothing is not a fault.
func HarpTopLevelArtifacts(harpDir string) ([]string, error) {
	entries, err := os.ReadDir(harpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read harp dir %q: %w", harpDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		if _, isMember := paths.ClassifyMember(e.Name()); isMember {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}
