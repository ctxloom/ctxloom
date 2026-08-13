package isolation

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// captureStderr swaps os.Stderr for a pipe; the returned func restores it and
// yields everything written meanwhile. For asserting the STREAMED half of a
// strictness fault (the warning fires in both modes; only recording is modal).
func captureStderr(t *testing.T) func() string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	return func() string {
		require.NoError(t, w.Close())
		os.Stderr = orig
		out, rerr := io.ReadAll(r)
		require.NoError(t, rerr)
		return string(out)
	}
}

// overrideContainer builds a run-as-is override Container (config
// isolation_images) over a fake runtime whose `image inspect` reports the
// image PRESENT and answers --format with configJSON — the hermetic stand-in
// for a user-supplied image with that .Config.
func overrideContainer(t *testing.T, configJSON, image string) Container {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, filepath.Join(dir, "builds.log"), dir, configJSON)
	marker := strings.NewReplacer("/", "_", ":", "_").Replace(image)
	require.NoError(t, os.WriteFile(filepath.Join(dir, marker), nil, 0o644))
	return containerFor(fakeRuntime{name: "docker", binary: script, available: true}, "claude-code", ImageConfig{Image: image})
}

// TestRunAsIsIdentityProblem pins the per-runtime identity contract for
// user-owned run-as-is images. PUID-passing modes (rootful docker, podman
// both modes) need the ctxloom entrypoint started as root — nothing else
// makes the PUID env change who the engine runs as. Rootless docker passes
// no PUID and container-root is the ONE uid that maps to the launching user,
// so there the image must simply run as root.
func TestRunAsIsIdentityProblem(t *testing.T) {
	governed := []string{"/usr/local/bin/ctxloom-entrypoint"}
	tests := []struct {
		name   string
		rt     Runtime
		id     imageIdentity
		wantOK bool
	}{
		{"rootful docker + governed", Docker{}, imageIdentity{Entrypoint: governed}, true},
		{"rootful docker + foreign entrypoint", Docker{}, imageIdentity{Entrypoint: []string{"/docker-entrypoint.sh"}}, false},
		{"rootful docker + no entrypoint", Docker{}, imageIdentity{}, false},
		{"rootful docker + governed but USER blocks the remap", Docker{}, imageIdentity{Entrypoint: governed, User: "node"}, false},
		{"rootless docker + default root", Docker{rootless: true}, imageIdentity{}, true},
		{"rootless docker + explicit root", Docker{rootless: true}, imageIdentity{User: "root"}, true},
		{"rootless docker + USER maps to a subuid", Docker{rootless: true}, imageIdentity{User: "1000:1000"}, false},
		{"rootful podman + governed", Podman{}, imageIdentity{Entrypoint: governed}, true},
		{"rootless podman + ungoverned", Podman{rootless: true}, imageIdentity{}, false},
		{"unknown runtime held to the PUID contract", fakeRuntime{}, imageIdentity{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			problem := runAsIsIdentityProblem(tt.rt, tt.id)
			if tt.wantOK {
				assert.Empty(t, problem)
			} else {
				assert.NotEmpty(t, problem)
			}
		})
	}
}

// TestCheckRunAsIsIdentity_UngovernedIsAFinding: a run-as-is override whose
// image would START with the wrong identity records a ClassIsolation finding
// (strict mode) — the container is never silently spawned wrong: the choke
// owner aborts on the finding BEFORE SpawnClient, exactly like the
// launch-failure gate it complements.
func TestCheckRunAsIsIdentity_UngovernedIsAFinding(t *testing.T) {
	resetStrictness(t)
	c := overrideContainer(t, `{"Entrypoint":["/docker-entrypoint.sh"],"User":""}`, "user/own:img")

	mark := strictness.Checkpoint()
	done := captureStderr(t)
	c.checkRunAsIsIdentity(context.Background())
	stderr := done()

	found := strictness.Since(mark)
	require.Len(t, found, 1, "wrong identity that STARTS must be a collected finding")
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	assert.Contains(t, found[0].Message, "user/own:img")
	assert.Contains(t, found[0].FixIt, "--degraded", "the fix-it names the escape hatch")
	assert.Contains(t, stderr, "user/own:img", "the warning streams in strict mode too")
}

// TestCheckRunAsIsIdentity_GovernedPasses: an override built on a ctxloom
// agent image (the baked entrypoint) satisfies the contract — no finding, no
// warning, zero behavior change for governed images.
func TestCheckRunAsIsIdentity_GovernedPasses(t *testing.T) {
	resetStrictness(t)
	c := overrideContainer(t, `{"Entrypoint":["/usr/local/bin/ctxloom-entrypoint"],"User":""}`, "user/governed:img")
	c.checkRunAsIsIdentity(context.Background())
	assert.Empty(t, strictness.All(), "a governed override runs exactly as before")
}

// TestCheckRunAsIsIdentity_UninspectableIsAFinding: an override whose config
// cannot be read (or parsed) cannot be verified — fail loud, never assume the
// contract holds.
func TestCheckRunAsIsIdentity_UninspectableIsAFinding(t *testing.T) {
	resetStrictness(t)
	c := overrideContainer(t, `not-json`, "user/odd:img")
	c.checkRunAsIsIdentity(context.Background())
	found := strictness.All()
	require.Len(t, found, 1)
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
	assert.Contains(t, found[0].Message, "cannot verify")
}

// TestCheckRunAsIsIdentity_DegradedWarnsAndProceeds: --degraded is the one
// warn-and-continue home — nothing is recorded (no gate can abort), but the
// warning still streams so a wrong-identity run is never invisible.
func TestCheckRunAsIsIdentity_DegradedWarnsAndProceeds(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	c := overrideContainer(t, `{"Entrypoint":null,"User":"node"}`, "user/own:img")

	done := captureStderr(t)
	c.checkRunAsIsIdentity(context.Background())
	stderr := done()

	assert.Empty(t, strictness.All(), "degraded records nothing")
	assert.Contains(t, stderr, "user/own:img", "the warning still streams")
}

// TestCheckRunAsIsIdentity_LocallyBuiltSkips: a backend with a local build
// recipe (no image override) bakes the entrypoint itself — the contract holds
// by construction and no inspect runs (the fake binary here would fail one).
func TestCheckRunAsIsIdentity_LocallyBuiltSkips(t *testing.T) {
	resetStrictness(t)
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "claude-code")
	c.checkRunAsIsIdentity(context.Background())
	assert.Empty(t, strictness.All(), "locally-built images are governed by construction")
}

// TestPrepareContainerScratch_GatesRunAsIsIdentity wires the check into the
// shared prepare front-half: the scratch still prepares (the abort decision
// belongs to the choke owner — strict gates on the finding pre-spawn,
// degraded proceeds), but the finding is recorded inside the caller's
// checkpoint window.
func TestPrepareContainerScratch_GatesRunAsIsIdentity(t *testing.T) {
	resetStrictness(t)
	// prepareContainerScratch no longer runs the shared-fs probe itself (it
	// moved to PrepareWorkspace, AFTER prepareBase, so it can see the REAL
	// mount roots) — this test drives prepareContainerScratch directly, so
	// there is no probe gate left in this call path to stub around.

	c := overrideContainer(t, `{"Entrypoint":null,"User":""}`, "user/own:img")
	c.engineSpec.resolveAuth = func(string, string) (containerAuth, bool) { return containerAuth{}, true }

	mark := strictness.Checkpoint()
	sc, err := c.prepareContainerScratch(context.Background())
	require.NoError(t, err, "prepare succeeds; refusal is the gate's, not the chain's")
	t.Cleanup(func() { _ = os.RemoveAll(sc.root) })

	found := strictness.Since(mark)
	require.Len(t, found, 1, "the wrong-identity finding lands inside the prepare window")
	assert.Equal(t, strictness.ClassIsolation, found[0].Class)
}
