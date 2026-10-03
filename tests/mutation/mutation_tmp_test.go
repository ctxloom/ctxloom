// No build tag, deliberately: mutation_tmp.sh is the one seam every mutation
// recipe releases its tool through, and a guard that only runs during the
// hour-long run it guards is not a guard.
package mutation

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An agent cell exports GOTMPDIR into its own session directory: deep, and
// inside the real app dir. A mutation tool's coverage run inherits whatever
// GOTMPDIR it is handed, and from there the suite's fixtures overflow the
// file-name limit and its sandbox assertions resolve into the real app dir.
// The wrapper must therefore REPLACE an inherited GOTMPDIR with the run's own
// scratch dir, which it already owns and removes.
func TestMutationTmp_ReplacesInheritedGOTMPDIRWithTheRunDir(t *testing.T) {
	script := repoInput(t, "tests/mutation/mutation_tmp.sh")[0]
	base := filepath.Join(t.TempDir(), "mutation")
	inherited := filepath.Join(t.TempDir(), "cell", "session", "deep", "gotmp")

	cmd := exec.Command("bash", script, base, "bash", "-c",
		`printf '%s\n%s\n' "$TMPDIR" "$GOTMPDIR"`)
	cmd.Env = append(os.Environ(), "GOTMPDIR="+inherited)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("mutation_tmp.sh: %v\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 2 {
		t.Fatalf("want TMPDIR and GOTMPDIR lines, got %q", out)
	}
	tmpdir, gotmpdir := lines[0], lines[1]
	if gotmpdir == inherited {
		t.Fatalf("GOTMPDIR inherited from the caller (%s); the tool must not see it", inherited)
	}
	if gotmpdir != tmpdir {
		t.Fatalf("GOTMPDIR = %q, want the run's own dir %q", gotmpdir, tmpdir)
	}
	if filepath.Dir(gotmpdir) != base {
		t.Fatalf("GOTMPDIR = %q, want a run dir directly under %q", gotmpdir, base)
	}
	if _, err := os.Stat(gotmpdir); !os.IsNotExist(err) {
		t.Fatalf("run dir %q survived the run (stat err %v)", gotmpdir, err)
	}
}
