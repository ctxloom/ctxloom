//go:build mutation

package mutation

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"testing"
)

// packageMutationTarget names ONE package for gremlins to mutate whole, judged
// by that package's own `go test`. It is the THIRD table, and a different tool
// from the other two: ooze mutates one FILE and needs a judge because the code
// under test runs in a subprocess (see the package doc); gremlins mutates a
// package and judges with the package's unit tests in-process, coverage-gated,
// which is exactly what the acceptance lane cannot do and exactly what a
// package whose behaviour lives in its unit tests needs.
//
// A standing entry here is ratcheted like the acceptance table — the same
// baseline file, the same `<tool>-target:` marker, the same ratchet — and
// unlike `just test-mutation-pkg PKG`, which runs the same tool over any path
// once and remembers nothing. Adding an entry is a data change: name, package,
// and a baseline row keyed TestPackageMutation/<Name>.
type packageMutationTarget struct {
	// Name is the subtest name: the -run address and the baseline key.
	// Single token, no slashes or spaces.
	Name string
	// Pkg is the package directory, slash-separated and relative to the module
	// root, with no leading "./" — the harness adds it when it hands the path
	// to gremlins.
	Pkg string
}

// packageMutationTargets is the table.
//
// The rule for a new entry is the inverse of the acceptance table's: the
// package's behaviour must be verified by ITS OWN unit tests, in-process,
// because that is all gremlins can see. A package whose tests exec a binary or
// drive a container scores NOT COVERED on every mutant it cannot reach — a true
// statement about the tests, and the ratchet holds it there, but the first run
// will say so at length.
var packageMutationTargets = []packageMutationTarget{
	{
		// The isolation package: runtime and workspace axis resolution, the
		// policy chain, the container transport, mount and identity plumbing,
		// worktree and container reaping. The acceptance lane's isolation_axes
		// entry mutates ONE file of it (isolation.go) and judges by the axis
		// matrix feature; everything else in the package is verified only by
		// its unit tests, which no ooze entry reaches.
		//
		// EVIDENCE: the package carries a unit test file beside nearly every
		// source file; the docker_integration- and integration-tagged tests are
		// invisible to gremlins (.gremlins.yaml runs untagged), so mutants in
		// the paths only those reach are expected NOT COVERED survivors.
		Name: "isolation",
		Pkg:  "internal/adapters/isolation",
	},
}

// gremlinsEfficacyThresholdExit is the exit status gremlins returns when a run
// completed and its efficacy fell at or below .gremlins.yaml's threshold
// (github.com/go-gremlins/gremlins/internal/execution, EfficacyThreshold). For
// this lane that is a MEASUREMENT, not a failure: the threshold names the
// project standard, the ratchet is the gate, and a gate that reds on every
// recorded debt would simply never be run — the exact failure the baseline
// file exists to avoid. The threshold itself is not touched; the miss is
// logged, and the tally goes to the ratchet.
const gremlinsEfficacyThresholdExit = 10

// release runs gremlins over the entry's package in the module at root,
// announcing the target first so the survivor ratchet can attribute the tally
// that follows. Marker and
// tally both go to out — os.Stdout in a real run, which is the stream the
// ratchet reads; a buffer under test, so a fake run never announces a target
// into a log the ratchet might be judging.
//
// There is no preflightThenRelease here, and none is needed: gremlins' first
// act is a coverage run of the package's own tests over the UNMUTATED tree, and
// any failure there aborts the run with a non-threshold exit before a mutant is
// made. TestPackageLane_RefusesATreeThatDoesNotBuild holds gremlins to that.
func (p packageMutationTarget) release(t *testing.T, root string, out io.Writer) {
	t.Helper()

	gremlins, err := exec.LookPath("gremlins")
	if err != nil {
		t.Fatalf("gremlins is not on PATH (%v) — `just test-mutation-install`", err)
	}

	// PER-TARGET ATTRIBUTION for the survivor ratchet, on the stream gremlins
	// prints its tally to, immediately before it starts — same reasoning as
	// the ooze marker in mutationTarget.release.
	fmt.Fprintf(out, "\ngremlins-target: %s\n", t.Name())

	// Dir is the module root so "./"+Pkg resolves and gremlins finds
	// .gremlins.yaml there (it searches the module root itself, but the
	// package argument is resolved against cwd). TMPDIR is whatever the recipe
	// pinned; gremlins copies the module once per worker and that must not
	// land on a tmpfs.
	cmd := exec.Command(gremlins, "unleash", "./"+p.Pkg)
	cmd.Dir = root
	cmd.Stdout = out
	cmd.Stderr = out
	t.Logf("gremlins unleash ./%s (cwd %s)", p.Pkg, root)

	err = cmd.Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() == gremlinsEfficacyThresholdExit:
		t.Logf("gremlins reports efficacy at or below .gremlins.yaml's threshold for ./%s; the survivor ratchet is this lane's verdict", p.Pkg)
	default:
		t.Fatalf("gremlins unleash ./%s: %v", p.Pkg, err)
	}
}

// TestPackageMutation runs gremlins over each entry of packageMutationTargets
// in turn, one subtest per entry. Ratcheted via `just test-mutation-package
// NAME`; run one entry alone with -run 'TestPackageMutation/^isolation$'.
//
// COST: gremlins copies the module once per worker, gathers coverage, then
// runs the package's tests once per covered mutant. Minutes to tens of
// minutes per package, not hours — but not a per-PR gate.
func TestPackageMutation(t *testing.T) {
	for _, target := range packageMutationTargets {
		t.Run(target.Name, func(t *testing.T) {
			target.release(t, repoRoot(t), os.Stdout)
		})
	}
}
