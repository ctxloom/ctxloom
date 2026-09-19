package isolation

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	claudeengine "github.com/ctxloom/ctxloom/internal/engines/claude/engine"
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
	claudeHome, ok := claudeengine.Descriptor().Home.Get()
	if !ok {
		panic("isolation tests: claude's descriptor declares no Home; the seed tests have nothing to exercise")
	}
	RegisterCredentialSeed(claude.EngineName, claudeHome.Credentials)
	// And its provisioning policy, which is LOAD-BEARING rather than setup
	// noise: it is the declaration Select walks to decide how the credential
	// reaches the instance. With it unregistered every seed would refuse, and
	// with a fixture standing in for it the tests would exercise an acceptance
	// order claude never declared.
	RegisterProvisioningPolicy(claude.EngineName, claudeengine.Descriptor().Provisioning)
	// And its container story with its shipping policy, for the same reason:
	// the spec the container tests build is the one claude declares.
	claudeDesc := claudeengine.Descriptor()
	RegisterEngineContainer(claude.EngineName, claudeDesc.Container, claudeDesc.Distribution)
	os.Exit(testsupport.SandboxedMain(m))
}

// claudeAuth returns the container-auth plan claude declares, as TestMain
// pushed it — what the auth tests hand resolveDeclaredAuth.
func claudeAuth(t *testing.T) agent.ContainerAuth {
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
func claudeSeed(t *testing.T) agent.CredentialSeed {
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

// claudeOverlayDirs returns the overlay set claude's declaration yields —
// its own config dir plus ctxloom's cache.
func claudeOverlayDirs(t *testing.T) []string {
	t.Helper()
	return engineContainerSpecFor(claude.EngineName).overlayDirs
}
