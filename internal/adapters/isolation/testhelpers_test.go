package isolation

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/mountns"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// testStamp is a whole, CLEAN version stamp — the shape version.ValidStamp
// accepts. It is fixed rather than read from the tree so a test's expectations
// cannot move with the build that runs them.
const testStamp = "v0.7.0-abc1234-20260904T031516"

// testStampDirty is the SAME commit built from a tracked-dirty tree.
const testStampDirty = testStamp + "-dirty"

// TestMain gives this package's tests the version stamp that production
// guarantees them. binaryVersion keys both the agent image tag and the
// ctxloom.provenance label, and internal/adapters/cli's root gate refuses to RUN an
// unstamped binary — so an unset stamp is a state production cannot reach.
// Leaving it unset here would silently empty the provenance and disable the
// staleness gate underneath every test that exercises it, which is a false
// green rather than a missing one.
func TestMain(m *testing.M) {
	// FIRST, before any other setup. This package's provisioner probe re-execs
	// os.Executable() to become a mount shim, and under `go test` that is THIS
	// binary. Without this line the shim re-runs the whole suite instead of
	// performing the mounts — which both breaks the probe and, because the
	// suite probes again, forks exponentially. Measured: the run never
	// terminated.
	mountns.RunChildIfRequested()
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
	// SandboxedMain closes config.findAppDir's walk-up from the working
	// directory for every test in this binary; a temp HOME alone does not.
	// Push claude's REAL credential-seed declaration through the same seam
	// backends uses. Registration became explicit (no init), and this package
	// CANNOT compose the registry the way other test binaries do: backends
	// imports isolation (delegate_seams.go), so importing backends or engines
	// from here is an import cycle. Calling the seam directly keeps the seed
	// these tests exercise the one the engine authors on its descriptor,
	// rather than a fixture that mirrors it and drifts.
	claudeKind, err := claude.Build()
	if err != nil {
		panic("isolation tests: " + err.Error())
	}
	if !claudeKind.Home().Relocates() {
		panic("isolation tests: claude's kind declares no Home; the seed tests have nothing to exercise")
	}
	// The kind's own declarations — its home (the seed and the deliveries it
	// accepts, which Select walks to decide how the credential reaches the
	// instance; a fixture standing in would exercise an acceptance order
	// claude never declared), its container story with its shipping policy
	// — through the one accessor.
	UseFacts(&overlayFacts{entries: map[string]EngineFacts{
		claude.EngineName: FactsOf(claudeKind),
	}})
	os.Exit(testsupport.SandboxedMain(m))
}

// overlayFacts is the test binary's Facts: staged entries over whatever the
// accessor answered before, so a test can declare a fixture engine (or
// re-declare one fact of a real one) for its own duration.
type overlayFacts struct {
	base    Facts
	entries map[string]EngineFacts
}

func (o *overlayFacts) For(name string) (EngineFacts, bool) {
	if f, ok := o.entries[name]; ok {
		return f, true
	}
	if o.base != nil {
		return o.base.For(name)
	}
	return EngineFacts{}, false
}

func (o *overlayFacts) Names() []string {
	seen := map[string]bool{}
	var names []string
	if o.base != nil {
		for _, n := range o.base.Names() {
			seen[n] = true
			names = append(names, n)
		}
	}
	for n := range o.entries {
		if !seen[n] {
			names = append(names, n)
		}
	}
	return names
}

// stageEngineFacts declares name's facts for the test's duration: mutate
// receives the facts the accessor answers today (zero for a new fixture
// name) and edits them. The previous accessor is restored on cleanup.
func stageEngineFacts(t *testing.T, name string, mutate func(f *EngineFacts)) {
	t.Helper()
	cur, _ := factsFor(name)
	mutate(&cur)
	factsMu.RLock()
	base := facts
	factsMu.RUnlock()
	restore := UseFacts(&overlayFacts{base: base, entries: map[string]EngineFacts{name: cur}})
	t.Cleanup(restore)
}

// claudeAuth returns the container-auth plan claude declares, as TestMain
// pushed it — what the auth tests hand resolveDeclaredAuth.
func claudeAuth(t *testing.T) engine.ContainerAuth {
	t.Helper()
	r, ok := engineContainerDeclared(claude.EngineName)
	require.True(t, ok, "fixture: claude's container declaration must be registered by TestMain")
	c, ok := r.container.Get()
	require.True(t, ok)
	a, ok := c.Auth.Get()
	require.True(t, ok)
	return a
}

// claudeSeed returns the seed claude declares, as TestMain pushed it — what
// every seed test here hands hostCredentialSeed.
func claudeSeed(t *testing.T) engine.CredentialSeed {
	t.Helper()
	seed, ok := credentialSeedFor(claude.EngineName)
	require.True(t, ok, "fixture: claude's credential seed must be registered by TestMain")
	return seed
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
// Several test files across this package use it, so it lives here rather than
// being duplicated per-file.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	t.Cleanup(restore)
	return &buf
}

// claudeOverlayDirs returns the overlay set claude's declaration yields —
// its own config dir plus ctxloom's cache.
func claudeOverlayDirs(t *testing.T) []string {
	t.Helper()
	return engineContainerSpecFor(claude.EngineName).overlayDirs
}

// copyAmbient is CopyAmbient with the report's Close bound to the test's end,
// the way the cell binds it to the run's (operations.prepareInTreeAmbient
// hands it back as the home's release). A seeded report leaves a credential
// replicator running; dropped, its watch goroutines outlive the test and warn
// through clidiag while a later test has swapped the sink out from under
// them — a data race the detector reports against whichever test is running.
func copyAmbient(t *testing.T, req AmbientRequest) (AmbientCopyReport, error) {
	t.Helper()
	report, err := CopyAmbient(req)
	t.Cleanup(func() { _ = report.Close() })
	return report, err
}
