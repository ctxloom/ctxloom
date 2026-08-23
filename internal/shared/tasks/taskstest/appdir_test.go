package taskstest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fatalRecorder stands in for *testing.T so requireIsolatedAppDir's refusal is
// OBSERVABLE. Wired to a real t, the only way to see the check fire is to fail
// the test asserting it, which means the interesting half — that it fires, and
// what it says — could never be pinned at all.
type fatalRecorder struct{ msgs []string }

func (r *fatalRecorder) Helper() {}

func (r *fatalRecorder) Fatalf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

func (r *fatalRecorder) only(t *testing.T) string {
	t.Helper()
	require.Len(t, r.msgs, 1, "expected exactly one refusal")
	return r.msgs[0]
}

// unratchetedPackage is a key deliberately absent from appDirEscapeRatchet, so
// these tests describe the DEFAULT — refuse — rather than whatever exemptions
// the ratchet happens to hold today.
const unratchetedPackage = "internal/no-such-package"

// TestRequireIsolatedAppDir_RefusesAnEscapingWorkingDirectory is the whole
// point of the seam: a temp HOME is not isolation. This test's own working
// directory is its package source directory, which sits under a checkout that
// has a real .ctxloom — exactly the shape that let a test run adopt a
// developer's app dir and overwrite their global config.
func TestRequireIsolatedAppDir_RefusesAnEscapingWorkingDirectory(t *testing.T) {
	isolateEnv(t) // temp HOME, no chdir: route 1 closed, route 2 wide open

	rec := &fatalRecorder{}
	requireIsolatedAppDir(rec, unratchetedPackage)

	msg := rec.only(t)
	ancestor := filepath.Join(realPath(t, repoRoot(t)), appDirName)
	assert.Contains(t, msg, ancestor,
		"the refusal must name the app dir findAppDir's walk-up would have adopted")
	assert.Contains(t, msg, realPath(t, os.TempDir()),
		"the refusal must name the temp root the escape is measured against")
	assert.Contains(t, msg, unratchetedPackage,
		"the refusal must name the package, since the package is the unit that gets fixed")
	assert.Contains(t, msg, "SandboxedMain",
		"the refusal must name the fix, not just the fault")
}

// TestRequireIsolatedAppDir_AcceptsASandboxedWorkingDirectory is the other
// half: the check must be satisfiable, or every caller would simply be forced
// onto the ratchet. A temp HOME plus a temp cwd is the state SandboxedMain
// installs process-wide and the state ProjectDir installs per test.
func TestRequireIsolatedAppDir_AcceptsASandboxedWorkingDirectory(t *testing.T) {
	isolateEnv(t)
	ChangeDir(t, t.TempDir())

	rec := &fatalRecorder{}
	requireIsolatedAppDir(rec, unratchetedPackage)

	assert.Empty(t, rec.msgs, "an isolated HOME and cwd must be accepted")
}

// TestRequireIsolatedAppDir_RatchetedPackageIsExempt pins the ratchet's ONLY
// job. Without it, landing the check turns two dozen packages red at once and
// the next person's morning is archaeology.
func TestRequireIsolatedAppDir_RatchetedPackageIsExempt(t *testing.T) {
	isolateEnv(t) // same escaping cwd as the refusal test above

	var exempt string
	for pkg := range appDirEscapeRatchet {
		exempt = pkg
		break
	}
	require.NotEmpty(t, exempt, "the ratchet is empty, so this test asserts nothing")

	rec := &fatalRecorder{}
	requireIsolatedAppDir(rec, exempt)

	assert.Empty(t, rec.msgs, "%s is on the ratchet and must not be refused", exempt)
}

// TestCallerPackage_NamesTheTestBinarysPackage pins the ratchet's KEY against
// the live stack: called from a test here, through a helper and a subtest, the
// answer is this package.
func TestCallerPackage_NamesTheTestBinarysPackage(t *testing.T) {
	assert.Equal(t, thisPackageRelative, callerPackage())

	t.Run("through a helper and a subtest", func(t *testing.T) {
		assert.Equal(t, thisPackageRelative, viaHelper())
	})
}

func viaHelper() string { return callerPackage() }

// TestCallerPackageFrom_KeysOnTheTestNotTheHelper is the case a live stack
// cannot express: a fixture in SOME OTHER package standing between Isolate and
// the test. Keying on the innermost caller would file every binary that uses
// such a fixture under the fixture's own package — one entry silently
// exempting packages nobody decided to exempt, and unfixable by fixing them.
func TestCallerPackageFrom_KeysOnTheTestNotTheHelper(t *testing.T) {
	prefix := repoPackagePrefix
	for _, tc := range []struct {
		name  string
		stack []string
		want  string
	}{
		{
			name: "a fixture in another package does not become the key",
			stack: []string{
				prefix + "internal/shared/tasks/taskstest.Isolate",
				prefix + "internal/fixtures/envfix.Setup",
				prefix + "internal/memory.TestStore",
				"testing.tRunner",
				"runtime.goexit",
			},
			want: "internal/memory",
		},
		{
			name: "the testsupport delegate is looked past",
			stack: []string{
				prefix + "internal/shared/tasks/taskstest.Isolate",
				prefix + "internal/testsupport.Isolate",
				prefix + "internal/config.TestLoad.func1",
				"testing.tRunner",
			},
			want: "internal/config",
		},
		{
			name: "an external test package folds into the package it tests",
			stack: []string{
				prefix + "internal/shared/tasks/taskstest.Isolate",
				prefix + "internal/paths_test.TestResolve",
				"testing.tRunner",
			},
			want: "internal/paths",
		},
		{
			name: "no testing frame falls back to the innermost foreign package",
			stack: []string{
				prefix + "internal/shared/tasks/taskstest.Isolate",
				prefix + "internal/testsupport.Isolate",
				prefix + "internal/sessions.setupMain",
			},
			want: "internal/sessions",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, callerPackageFrom(tc.stack))
		})
	}
}

// ratchetEntryFacts is what the repository says about one ratchet entry.
type ratchetEntryFacts struct {
	exists         bool
	adoptedSandbox bool
	callsIsolate   bool
	escapesAppDir  bool
}

// staleReason names why an entry no longer exempts anything, or "" while it
// still does. It is a pure function over the four facts so every arm can be
// driven — including "the package no longer has an app dir above it", which
// no entry in a checkout that ships its own .ctxloom can currently exhibit,
// and which would otherwise be an assertion nothing could ever fail.
func (f ratchetEntryFacts) staleReason() string {
	switch {
	case !f.exists:
		return "there is no such package"
	case f.adoptedSandbox:
		return "it has adopted the process-wide sandbox"
	case !f.callsIsolate:
		return "it no longer calls the isolation helpers, so its exemption reaches nothing"
	case !f.escapesAppDir:
		return "it has no app dir above it outside the temp root, so nothing about it escapes"
	}
	return ""
}

func TestRatchetEntryFacts_StaleReason(t *testing.T) {
	live := ratchetEntryFacts{exists: true, callsIsolate: true, escapesAppDir: true}
	assert.Empty(t, live.staleReason(), "a package that still escapes and still isolates is live")

	for _, tc := range []struct {
		name  string
		facts ratchetEntryFacts
		want  string
	}{
		{"deleted package", ratchetEntryFacts{}, "there is no such package"},
		{"swept onto the sandbox", ratchetEntryFacts{exists: true, adoptedSandbox: true, callsIsolate: true, escapesAppDir: true}, "it has adopted the process-wide sandbox"},
		{"stopped isolating", ratchetEntryFacts{exists: true, escapesAppDir: true}, "it no longer calls the isolation helpers, so its exemption reaches nothing"},
		{"nothing left to escape into", ratchetEntryFacts{exists: true, callsIsolate: true}, "it has no app dir above it outside the temp root, so nothing about it escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.facts.staleReason())
		})
	}
}

// TestAppDirEscapeRatchet_IsLive is the ratchet's liveness twin, in the shape
// of TestArch_LayeringAllowlist_IsLive: an exemption that no longer exempts
// anything must FAIL, not sit there. A stale entry is worse than no entry —
// it reads as a known, tracked hazard while covering nothing, and it lets the
// list quietly stop shrinking, which is the one thing a ratchet must not do.
func TestAppDirEscapeRatchet_IsLive(t *testing.T) {
	root := repoRoot(t)
	tempRoot := os.TempDir()

	for pkg := range appDirEscapeRatchet {
		t.Run(pkg, func(t *testing.T) {
			facts := observeRatchetEntry(t, filepath.Join(root, filepath.FromSlash(pkg)), tempRoot)
			assert.Emptyf(t, facts.staleReason(),
				"%s is on appDirEscapeRatchet, but %s; delete the entry", pkg, facts.staleReason())
		})
	}
}

// observeRatchetEntry reads the four facts staleReason judges off disk.
func observeRatchetEntry(t *testing.T, dir, tempRoot string) ratchetEntryFacts {
	t.Helper()
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ratchetEntryFacts{}
	}
	facts := ratchetEntryFacts{exists: true}

	sources := testSourcesIn(t, dir)
	facts.adoptedSandbox = anyContains(sources, sandboxedMainMarker)
	facts.callsIsolate = anyContains(sources, "Isolate(", "ProjectDir(")

	esc, err := escapingAppDirAncestor(dir, tempRoot)
	require.NoError(t, err)
	facts.escapesAppDir = esc != ""
	return facts
}

// sandboxedMainMarker is assembled rather than written out because this file
// is itself scanned by nothing — but the packages it scans include the two
// that DOCUMENT the sweep in prose (taskstest and testsupport both quote the
// TestMain one-liner). Restricting the scan to _test.go files is what keeps
// those docs from reading as adoption; the split spelling keeps a future
// reader from "fixing" the marker into a form that matches a doc comment.
var sandboxedMainMarker = "testsupport.SandboxedMain" + "(m)"

// testSourcesIn reads the package's _test.go files, keyed by base name. Only
// test files: the sweep is a TestMain, and a production file that merely
// names SandboxedMain in a doc comment has adopted nothing.
func testSourcesIn(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	out := map[string]string{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		require.NoError(t, err)
		out[e.Name()] = string(b)
	}
	return out
}

func anyContains(sources map[string]string, needles ...string) bool {
	for _, src := range sources {
		for _, needle := range needles {
			if strings.Contains(src, needle) {
				return true
			}
		}
	}
	return false
}

func realPath(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	require.NoError(t, err)
	return real
}
