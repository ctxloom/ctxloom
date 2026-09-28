//go:build !windows

// overrideContainer drives writeFakeRuntimeScript's #!/bin/sh runtime stub, which a Windows host cannot exec.

package isolation

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

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

// TestCheckRunAsIsIdentity_UngovernedIsAFinding: a run-as-is override whose
// image would START with the wrong identity records a ClassIsolation finding
// (strict mode) — the container is never silently spawned wrong: the choke
// owner aborts on the finding BEFORE StartRunner, exactly like the
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
	assert.Equal(t, report.KindIsolation, found[0].Kind)
	assert.Contains(t, found[0].Text, "user/own:img")
	// Inverted by the degradation audit: this finding is non-degradable, so a
	// fix-it naming --degraded would be a remedy that does not work.
	assert.True(t, found[0].NonDegradable, "a wrong-identity image writes root-owned files: refused in both modes")
	assert.NotContains(t, found[0].Remedy, "--degraded", "a non-degradable refusal must not offer --degraded as its remedy")
	assert.Contains(t, found[0].Remedy, "ctxloom container build", "it must name a route that actually fixes the identity")
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
	assert.Equal(t, report.KindIsolation, found[0].Kind)
	assert.Contains(t, found[0].Text, "cannot be verified")
}

// TestCheckRunAsIsIdentity_DegradedWarnsAndProceeds: --degraded is the one
// warn-and-continue home — the finding IS recorded (degraded suppresses
// fatality, not recording) but no gate acts on it, and the warning still
// streams so a wrong-identity run is never invisible.
func TestCheckRunAsIsIdentity_DegradedWarnsAndProceeds(t *testing.T) {
	resetStrictness(t)
	c := overrideContainer(t, `{"Entrypoint":null,"User":"node"}`, "user/own:img")

	done := captureStderr(t)
	c.checkRunAsIsIdentity(context.Background())
	stderr := done()

	assert.NotEmpty(t, strictness.All(),
		"degraded suppresses fatality, not recording: the finding is kept so the run can account for what it skipped")
	assert.Contains(t, stderr, "user/own:img", "the warning still streams")
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

	testsupport.Isolate(t)
	c := overrideContainer(t, `{"Entrypoint":null,"User":""}`, "user/own:img").
		WithSessionState(SessionState{Harp: "brisk-teal-otter"})

	mark := strictness.Checkpoint()
	sc, err := c.prepareContainerScratch(context.Background())
	require.NoError(t, err, "prepare succeeds; refusal is the gate's, not the chain's")
	t.Cleanup(func() { _ = os.RemoveAll(sc.root) })

	found := strictness.Since(mark)
	require.Len(t, found, 1, "the wrong-identity finding lands inside the prepare window")
	assert.Equal(t, report.KindIsolation, found[0].Kind)
}
