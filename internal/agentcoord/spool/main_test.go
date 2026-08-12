package spool

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// Probe-mode environment for the container side of the docker-gated
// cross-mount test. It lives in this UNTAGGED file, with TestMain, because Go
// allows exactly one TestMain per package: keeping the probe entry point in
// the docker_integration-tagged file would mean two of them under that tag.
// The probe itself needs only this package's own API, so nothing about it is
// docker-specific.
const (
	envProbe       = "CTXLOOM_SPOOL_PROBE"
	envProbeHarp   = "CTXLOOM_SPOOL_PROBE_HARP"
	envProbeMarker = "CTXLOOM_SPOOL_PROBE_MARKER"
)

// realHOME is the ambient HOME this process started with, captured at init —
// before TestMain's sandbox overwrites it.
//
// Any test that shells out to `go` needs it: the Go toolchain derives GOPATH
// and its module cache from $HOME, so a child build inheriting the sandbox
// home rebuilds the whole module cache inside a throwaway directory that
// os.RemoveAll then cannot remove (the cache marks its directories
// read-only). internal/operations' TestMain carries the same variable for the
// same measured reason, and this package has already paid that bill once —
// see the note on buildProbe.
var realHOME = os.Getenv("HOME")

// TestMain sandboxes the whole package run and then GUARDS the package source
// directory.
//
// Why the sandbox: a test binary runs with cwd = its own package source dir,
// and several ctxloom path resolvers fall back to a cwd-relative ".ctxloom"
// (paths.ProjectSessionsDir("") -> <cwd>/.ctxloom/sessions is the plainest
// one). Nothing in THIS package resolves such a path today — but nothing
// stops a future test from reaching one through a helper, and the failure is
// silent: a directory appears in the source tree, .gitignore hides it from
// `git status`, and it goes on confusing worktree-safe WIP detection until
// `just test`'s leak check fails on someone else's branch.
// testsupport.SandboxedMain is the repo's established answer (it moves HOME
// and cwd into a temp sandbox and fails closed if the isolation does not
// hold), so this package uses it rather than inventing a second discipline.
//
// Why the guard as well: the sandbox covers relative paths, not a test that
// builds an ABSOLUTE path into the source tree — which is exactly what the
// docker-gated test's fixture used to do. A guard that names the leak, deletes
// it, and fails the run turns "silent disk residue discovered three branches
// later" into a red test in the package that caused it.
func TestMain(m *testing.M) {
	if phase := os.Getenv(envProbe); phase != "" {
		// Container side: never runs tests, so it must not sandbox or guard.
		os.Exit(runProbe(phase))
	}

	// Captured BEFORE SandboxedMain chdirs away, and from the source path
	// rather than the cwd, so the guard still names the right directory.
	pkgDir := packageSourceDir()

	code := testsupport.SandboxedMain(m)

	if leaks := sourceTreeLeaks(pkgDir); len(leaks) > 0 {
		for _, leak := range leaks {
			_ = os.RemoveAll(leak)
		}
		fmt.Fprintf(os.Stderr,
			"spool test isolation FAILED: a test wrote into the package source dir instead of a "+
				"temp dir: %v\nThe entries were removed. Fixtures belong under t.TempDir() or, when a "+
				"test needs a non-tmpfs path, under crossMountFixtureRoot() — never inside the checkout.\n",
			leaks)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}

// leakGlobs are the source-tree entries this package must never produce. The
// first is what `just test`'s _check-no-ctxloom-leak scans for; the second is
// this package's own cross-mount fixture, whose earlier version really did
// land in the checkout.
var leakGlobs = []string{".ctxloom", ".spool-xmount-*"}

// sourceTreeLeaks returns any leaked entries found directly under dir.
func sourceTreeLeaks(dir string) []string {
	if dir == "" {
		return nil
	}
	var found []string
	for _, pattern := range leakGlobs {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			continue
		}
		found = append(found, matches...)
	}
	return found
}

// packageSourceDir returns this package's source directory, derived from the
// compiled-in file path so it is correct no matter what the working directory
// is when it is called.
func packageSourceDir() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return ""
	}
	return filepath.Dir(file)
}

// crossMountFixtureRoot returns the parent directory for the docker-gated
// test's fixture home.
//
// Two constraints, and they pull in different directions. The fixture must be
// OUTSIDE the source tree: `just test`'s leak check scans the checkout, and an
// in-tree fixture is disk residue that confuses worktree-safe WIP detection
// even when .gitignore hides it. It should also be on a REAL filesystem: /tmp
// is tmpfs on a stock Linux box, and a durable message substrate proven only
// over RAM is evidence about the wrong thing. /var/tmp satisfies both — it is
// disk-backed by convention (and by measurement here: ext4) and it is nobody's
// source tree.
//
// The env override exists for a machine where /var/tmp is unusable; falling
// back to os.TempDir() keeps the test runnable there, trading the filesystem
// property for the isolation property, which is the right way round because
// the isolation one is what a gate enforces.
func crossMountFixtureRoot() string {
	if custom := os.Getenv("CTXLOOM_SPOOL_FIXTURE_ROOT"); custom != "" {
		return custom
	}
	const preferred = "/var/tmp"
	if st, err := os.Stat(preferred); err == nil && st.IsDir() {
		return preferred
	}
	return os.TempDir()
}

// TestCrossMountFixtureRoot_IsOutsideTheSourceTree pins the property the leak
// gate cares about, in the DEFAULT lane — where the gate actually runs.
//
// The docker-gated test that consumes this root only runs with a daemon and a
// build tag, so a regression there would be invisible to `just test` until it
// had already written into the checkout. This test is what makes "the fixture
// lives outside the source tree" a fact the default lane can refuse to lose.
func TestCrossMountFixtureRoot_IsOutsideTheSourceTree(t *testing.T) {
	root := crossMountFixtureRoot()
	if !filepath.IsAbs(root) {
		t.Fatalf("fixture root %q must be absolute: a relative root resolves against the test binary's cwd, which IS the package source dir", root)
	}

	pkgDir := packageSourceDir()
	if pkgDir == "" {
		t.Fatal("could not derive the package source dir")
	}
	repo := filepath.Dir(filepath.Dir(filepath.Dir(pkgDir))) // internal/agentcoord/spool -> repo root
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		t.Fatalf("derived repo root %q does not contain go.mod: %v", repo, err)
	}

	for _, forbidden := range []string{pkgDir, repo} {
		if isInside(forbidden, root) {
			t.Fatalf("fixture root %q is inside the source tree %q: the leak gate scans the checkout", root, forbidden)
		}
	}

	st, err := os.Stat(root)
	if err != nil {
		t.Fatalf("fixture root %q must exist and be usable: %v", root, err)
	}
	if !st.IsDir() {
		t.Fatalf("fixture root %q is not a directory", root)
	}
}

// isInside reports whether child is parent or lives beneath it.
func isInside(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	if err != nil {
		return false
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// TestSourceTreeLeaks_DetectsWhatTheGateScansFor proves the guard above can go
// red. A guard nobody has seen fail is a guard nobody knows works.
func TestSourceTreeLeaks_DetectsWhatTheGateScansFor(t *testing.T) {
	dir := t.TempDir()
	if leaks := sourceTreeLeaks(dir); len(leaks) != 0 {
		t.Fatalf("clean dir reported leaks: %v", leaks)
	}

	if err := os.MkdirAll(filepath.Join(dir, ".ctxloom", "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".spool-xmount-123"), 0o700); err != nil {
		t.Fatal(err)
	}
	leaks := sourceTreeLeaks(dir)
	if len(leaks) != 2 {
		t.Fatalf("guard must report both leak shapes, got %v", leaks)
	}
}

// runProbe is the container side. It prints machine-readable lines the host
// asserts on and returns a non-zero status with a reason on any failure —
// a probe that exited 0 having checked nothing is the failure mode this whole
// suite exists to catch.
func runProbe(phase string) int {
	harp := os.Getenv(envProbeHarp)
	marker := os.Getenv(envProbeMarker)
	if phase != "roundtrip" {
		fmt.Fprintf(os.Stderr, "probe: unknown phase %q\n", phase)
		return 2
	}
	if harp == "" || marker == "" {
		fmt.Fprintf(os.Stderr, "probe: %s and %s are required\n", envProbeHarp, envProbeMarker)
		return 2
	}
	m := NewHomeMapper()

	// 1. The host wrote one in/ message and consumed it. In-container, in/
	//    must be EMPTY and in/consumed/ must hold it.
	live, err := Sweep(m, harp, DirIn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: sweeping in/: %v\n", err)
		return 1
	}
	if err := live.ProblemErr(); err != nil {
		fmt.Fprintf(os.Stderr, "probe: in/ has unreadable files: %v\n", err)
		return 1
	}
	if len(live.Entries) != 0 {
		fmt.Fprintf(os.Stderr, "probe: in/ should be empty after the host consumed, found %d\n", len(live.Entries))
		return 1
	}
	consumed, err := Sweep(m, harp, DirInConsumed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: sweeping in/consumed: %v\n", err)
		return 1
	}
	if err := consumed.ProblemErr(); err != nil {
		fmt.Fprintf(os.Stderr, "probe: in/consumed has unreadable files: %v\n", err)
		return 1
	}
	if len(consumed.Entries) != 1 {
		fmt.Fprintf(os.Stderr, "probe: in/consumed should hold exactly the consumed message, found %d\n", len(consumed.Entries))
		return 1
	}
	wantBody := marker + "-in\n"
	if got := consumed.Entries[0].Message.Body; got != wantBody {
		fmt.Fprintf(os.Stderr, "probe: consumed body %q, want %q\n", got, wantBody)
		return 1
	}
	consumedPath, err := m.Resolve(consumed.Entries[0].Ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: resolving the consumed ref: %v\n", err)
		return 1
	}
	fmt.Printf("PROBE_CONSUMED_PATH=%s\n", consumedPath)

	// 2. Write one out/ message for the host to read back.
	w, err := NewWriter(m, harp, DirOut, "agentprobe")
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: creating the out writer: %v\n", err)
		return 1
	}
	ref, err := w.Write(&Message{Kind: "report", FromHarp: harp, To: "parent", Body: marker + "-out\n"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: writing out/: %v\n", err)
		return 1
	}
	outPath, err := m.Resolve(ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "probe: resolving the written ref: %v\n", err)
		return 1
	}
	fmt.Printf("PROBE_OUT_NAME=%s\n", ref.Name)
	fmt.Printf("PROBE_OUT_PATH=%s\n", outPath)
	fmt.Println("PROBE_OK")
	return 0
}
