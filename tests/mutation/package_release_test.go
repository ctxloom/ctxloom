//go:build mutation

package mutation

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGremlins puts a stand-in `gremlins` first on PATH that prints a tally
// and exits with the given status, so release's verdict on an exit code can
// be pinned without a real run.
func fakeGremlins(t *testing.T, exit string) {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"fake gremlins $*\"\necho \"Killed: 1, Lived: 1, Not covered: 0\"\nexit " + exit + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gremlins"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gremlins: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

var releaseProbe = packageMutationTarget{Name: "probe", Pkg: "internal/adapters/isolation"}

// The marker precedes the tally on the same writer: that ordering is the whole
// contract with the ratchet, which attributes a tally to the marker before it.
func TestPackageRelease_AnnouncesTheTargetBeforeTheTally(t *testing.T) {
	fakeGremlins(t, "0")
	var out bytes.Buffer

	ok := t.Run("probe", func(t *testing.T) { releaseProbe.release(t, repoRoot(t), &out) })
	if !ok {
		t.Fatalf("release failed on exit 0; output:\n%s", out.String())
	}

	got := out.String()
	marker := strings.Index(got, "gremlins-target: TestPackageRelease_AnnouncesTheTargetBeforeTheTally/probe\n")
	tally := strings.Index(got, "Killed: 1, Lived: 1")
	if marker < 0 || tally < 0 || marker > tally {
		t.Errorf("marker must precede the tally on the same stream; got:\n%s", got)
	}
	if !strings.Contains(got, "fake gremlins unleash ./internal/adapters/isolation") {
		t.Errorf("gremlins must be handed \"./\"+Pkg; got:\n%s", got)
	}
}

// An efficacy-threshold exit is a MEASUREMENT for this lane: the ratchet is
// the gate. Any other nonzero exit is a run that did not complete and fails.
func TestPackageRelease_ThresholdExitIsMeasuredOtherExitsFail(t *testing.T) {
	fakeGremlins(t, "10")
	var out bytes.Buffer
	if ok := t.Run("threshold", func(t *testing.T) { releaseProbe.release(t, repoRoot(t), &out) }); !ok {
		t.Errorf("exit 10 (efficacy at or below threshold) must not fail the lane — the ratchet judges the tally; output:\n%s", out.String())
	}

	fakeGremlins(t, "1")
	out.Reset()
	// A detached T, as TestBuildIgnorePattern_RefusesAnUnknownTarget does: the
	// expected Fatalf must not fail THIS test. Fatalf runtime.Goexit()s the
	// goroutine; the deferred close still runs.
	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		releaseProbe.release(fake, repoRoot(t), &out)
	}()
	<-done
	if !fake.Failed() {
		t.Errorf("exit 1 is a run that did not complete and must fail; output:\n%s", out.String())
	}
}

// The package lane has no preflightThenRelease because gremlins IS its own:
// it gathers coverage by running the package's tests over the unmutated tree
// before making a single mutant, and a failure there — a tree that does not
// build, or a test that already fails — ends the run with exit 1. This holds
// the REAL gremlins to that on a package that does not compile, through the
// lane's own release: a gremlins that reported such a tree as a measurement
// (exit 0, or the threshold exit) would release mutants a broken build scores.
func TestPackageLane_RefusesATreeThatDoesNotBuild(t *testing.T) {
	if _, err := exec.LookPath("gremlins"); err != nil {
		t.Fatalf("gremlins is not on PATH (%v) — `just test-mutation-install`", err)
	}
	src := fakeModule(t, map[string]string{
		"go.mod":                labGoMod,
		"broken/broken.go":      "package broken\n\nfunc Add(a, b int) int { return a + b + notDeclared }\n",
		"broken/broken_test.go": "package broken\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"add\")\n\t}\n}\n",
	})
	// gremlins copies the module per worker under TMPDIR; keep it in the test's.
	t.Setenv("TMPDIR", t.TempDir())

	var out bytes.Buffer
	fake := &testing.T{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		packageMutationTarget{Name: "broken", Pkg: "broken"}.release(fake, src, &out)
	}()
	<-done

	got := out.String()
	if !fake.Failed() {
		t.Errorf("the package lane accepted gremlins' verdict on a package that does not build; output:\n%s", got)
	}
	// Failed for THIS reason: gremlins' coverage run refused the tree. Any other
	// failure (a missing binary, a bad path) would pass the check above.
	if !strings.Contains(got, "failed to gather coverage") {
		t.Errorf("expected gremlins' coverage run to refuse the tree; output:\n%s", got)
	}
	if strings.Contains(got, "Killed: ") {
		t.Errorf("gremlins produced a tally over a tree that does not build; output:\n%s", got)
	}
}
