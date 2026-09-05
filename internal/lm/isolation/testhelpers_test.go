package isolation

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// testStamp is a whole, CLEAN version stamp — the shape version.ValidStamp
// accepts. It is fixed rather than read from the tree so a test's expectations
// cannot move with the build that runs them.
const testStamp = "v0.7.0-abc1234-20260904T031516"

// testStampDirty is the SAME commit built from a tracked-dirty tree.
const testStampDirty = testStamp + "-dirty"

// TestMain gives this package's tests the version stamp that production
// guarantees them. binaryVersion keys both the agent image tag and the
// ctxloom.provenance label, and internal/cli's root gate refuses to RUN an
// unstamped binary — so an unset stamp is a state production cannot reach.
// Leaving it unset here would silently empty the provenance and disable the
// staleness gate underneath every test that exercises it, which is a false
// green rather than a missing one.
func TestMain(m *testing.M) {
	SetBinaryVersion(testStamp)
	// The provenance key also covers the STAGED COMPANIONS' versions
	// (companionVersionKey), which would otherwise exec whatever taskloom /
	// ltk / reprise happen to be installed on the machine running the suite —
	// making every provenance assertion in this package a function of the
	// developer's PATH, and letting a broken local companion raise a fatal
	// finding inside tests that have nothing to do with companions. The
	// default fixture is therefore "none installed"; the tests that exercise
	// the companion half install their own (withCompanions).
	companionLookPath = noCompanionsOnPath
	os.Exit(m.Run())
}

// noCompanionsOnPath is the TestMain default: no companion resolves.
func noCompanionsOnPath(string) (string, error) { return "", exec.ErrNotFound }

// withRealCompanionLookPath restores the production PATH lookup for one test,
// for the tests that drive companion resolution through a fabricated PATH
// (t.Setenv) rather than through the fixture. Without it TestMain's
// no-companions default silently answers first and the PATH those tests built
// is never consulted.
func withRealCompanionLookPath(t *testing.T) {
	t.Helper()
	orig := companionLookPath
	companionLookPath = exec.LookPath
	t.Cleanup(func() { companionLookPath = orig })
}

// withCompanions installs a companion fixture for one test: name -> reported
// version, where a version of "" means the binary is PRESENT on PATH but its
// version probe FAILS. Restored on cleanup.
func withCompanions(t *testing.T, versions map[string]string) {
	t.Helper()
	origLook, origProbe := companionLookPath, companionVersionProbe
	paths := map[string]string{}
	for name := range versions {
		paths["/fake/bin/"+name] = name
	}
	companionLookPath = func(name string) (string, error) {
		if _, ok := versions[name]; !ok {
			return "", exec.ErrNotFound
		}
		return "/fake/bin/" + name, nil
	}
	companionVersionProbe = func(path string) (string, error) {
		name, ok := paths[path]
		if !ok {
			return "", exec.ErrNotFound
		}
		if versions[name] == "" {
			return "", errors.New("version --format json output has no version field")
		}
		return versions[name], nil
	}
	t.Cleanup(func() { companionLookPath, companionVersionProbe = origLook, origProbe })
}

// captureWarnings redirects clidiag's stderr-flavored helpers (Warn/WarnOnce)
// to a buffer for the duration of the test and returns it, so a test can
// assert on a LOUD non-fatal finding's text without touching strictness.
//
// Formerly lived in curatedhome_test.go (deleted with the antigravity-only
// curated-HOME mechanism); several unrelated test files across this package
// still use it, so it moved here rather than being duplicated per-file.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}
