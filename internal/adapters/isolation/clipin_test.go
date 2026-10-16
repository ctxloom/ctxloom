//go:build !windows

// The build tests drive writeFakeRuntimeScript, a #!/bin/sh runtime stub a
// Windows host cannot exec.

package isolation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// recordingProbe answers every engine with (version, err) and records which
// engines it was asked about.
type recordingProbe struct {
	mu      sync.Mutex
	version string
	err     error
	asked   []string
}

func (p *recordingProbe) probe(_ context.Context, engine string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.asked = append(p.asked, engine)
	return p.version, p.err
}

func (p *recordingProbe) engines() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.asked...)
}

// withHostCLI installs a probe answering version (or err) for the test's
// duration and returns it.
func withHostCLI(t *testing.T, version string, err error) *recordingProbe {
	t.Helper()
	p := &recordingProbe{version: version, err: err}
	t.Cleanup(UseHostCLIVersionProbe(p.probe))
	return p
}

// absentHostCLI is the host with no claude CLI on it.
var absentHostCLI = &engineversion.BinaryAbsentError{Engine: "claude-code", Err: errors.New("not on PATH")}

// TestComposedImageTagFor_CLIPinIsInTheTag: the host CLI version is part of
// the image's identity, so a host upgrade lands on a NEW tag (absent,
// therefore built) instead of reusing an image carrying the old CLI. No pin
// leaves the tag exactly as it was before pinning existed.
func TestComposedImageTagFor_CLIPinIsInTheTag(t *testing.T) {
	base := []byte("FROM debian:13\n")
	const engine, vk = "claude-code", "v0.7.0-abc1234"

	a := composedImageTagFor(base, engine, vk, "2.1.280")
	b := composedImageTagFor(base, engine, vk, "2.1.281")
	assert.NotEqual(t, a, b, "only the host CLI version changed; the tag must change with it")
	assert.Equal(t, a, composedImageTagFor(base, engine, vk, "2.1.280"), "one CLI version must resolve to one tag")
	assert.Equal(t, "ctxloom-agent-claude-code:"+vk+"-cli2.1.280-"+composedContentHash(base, engine), a,
		"the pinned version is readable in `docker images`")
	assert.Equal(t, "ctxloom-agent-claude-code:"+vk+"-"+composedContentHash(base, engine),
		composedImageTagFor(base, engine, vk, ""), "an unpinned image keeps the pre-pin tag")
}

// TestComposeAgentContainerfile_PinsTheHostCLIVersion: the install renders the
// exact host version, and the image carries a label naming it — BELOW the
// install, so the label never invalidates the engine layer on its own.
func TestComposeAgentContainerfile_PinsTheHostCLIVersion(t *testing.T) {
	cf := string(composeAgentContainerfile("claude-code", "2.1.280"))

	install := strings.Index(cf, "npm install -g @anthropic-ai/claude-code@2.1.280 ")
	require.Positive(t, install, "the engine install must pin the host's CLI version")
	label := strings.Index(cf, `LABEL `+engineCLIVersionLabel+`="2.1.280"`)
	require.Positive(t, label, "the image must say which CLI version it carries")
	assert.Greater(t, label, install)

	unpinned := string(composeAgentContainerfile("claude-code", ""))
	assert.NotContains(t, unpinned, "@anthropic-ai/claude-code@")
	assert.NotContains(t, unpinned, engineCLIVersionLabel, "an unpinned image claims no version")
}

// TestContainerFor_HostCLIUpgradeStalesTheImage drives the run's real entry
// point: the tag a launch looks up, the provenance its staleness gate compares,
// and the Containerfile its build would run all follow the host CLI version.
func TestContainerFor_HostCLIUpgradeStalesTheImage(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}
	img := ImageConfig{Base: "ctxloom"}

	probe := withHostCLI(t, "2.1.280", nil)
	before := containerFor(rt, "claude-code", img)
	beforeID := before.identityFor(defaultBaseStage())
	sources, _, err := before.containerBuildSources("")
	require.NoError(t, err)
	require.Len(t, sources, 1)
	assert.Contains(t, string(sources[0].containerfile), "@anthropic-ai/claude-code@2.1.280 ",
		"the build the launch would run installs the version its tag names")
	assert.Contains(t, before.image, "-cli2.1.280-")

	same := containerFor(rt, "claude-code", img)
	assert.Equal(t, before.image, same.image, "an unchanged host CLI reuses the image")

	probe.version = "2.1.281"
	after := containerFor(rt, "claude-code", img)
	assert.NotEqual(t, before.image, after.image, "a host CLI upgrade must land on a new tag")
	assert.True(t, imageStale(map[string]string{provenanceLabel: beforeID.provenance}, after.identityFor(defaultBaseStage()).provenance),
		"an image built for the old host CLI must read as stale")
}

// TestBuildAgentImage_TagsTheHostPin: the explicit build lands on the tag a
// launch resolves under the same host CLI.
func TestBuildAgentImage_TagsTheHostPin(t *testing.T) {
	withHostCLI(t, "2.1.280", nil)
	withFakeSelfExe(t)
	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	script := filepath.Join(dir, "docker")
	writeFakeRuntimeScript(t, script, logFile, dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}
	stubBuildRuntimeProbe(t, rt)

	built, err := BuildAgentImage(context.Background(), "claude-code", ImageBuildOptions{Base: "ctxloom"})
	require.NoError(t, err)
	assert.Contains(t, built, "-cli2.1.280-")
	assert.Equal(t, containerFor(rt, "claude-code", ImageConfig{Base: "ctxloom"}).image, built,
		"the explicit build and the run must resolve the same tag")
	assert.Contains(t, strings.Join(buildInvocations(t, logFile), "\n"), "-t "+built)
}

// TestHostCLIPin_OnlyWhereCtxloomInstallsAPinningEngine: the host is asked
// only for an engine that declares a pinned install, and only when ctxloom
// itself installs the engine. A user-owned image (isolation_images) or an
// overlay onto a base that already ships the client is the user's version.
func TestHostCLIPin_OnlyWhereCtxloomInstallsAPinningEngine(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}

	t.Run("an engine without a pinned install is never probed", func(t *testing.T) {
		probe := withHostCLI(t, "9.9.9", nil)
		stageEngineFacts(t, "unpinned-engine", func(f *EngineFacts) {
			f.Container = engine.Provide(engine.ContainerSpec{Install: []byte("RUN true\n"), ValidateCommand: "true"})
		})
		c := containerFor(rt, "unpinned-engine", ImageConfig{Base: "ctxloom"})
		assert.Empty(t, probe.engines())
		assert.NotContains(t, c.image, "-cli")
	})

	t.Run("a user-owned image is never probed", func(t *testing.T) {
		probe := withHostCLI(t, "2.1.280", nil)
		c := containerFor(rt, "claude-code", ImageConfig{Image: "mine/claude:1"})
		assert.Empty(t, probe.engines())
		assert.Equal(t, "mine/claude:1", c.image)
	})

	t.Run("an overlay image is never probed", func(t *testing.T) {
		probe := withHostCLI(t, "2.1.280", nil)
		withFakeSelfExe(t)
		dir := t.TempDir()
		script := filepath.Join(dir, "docker")
		writeFakeRuntimeScript(t, script, filepath.Join(dir, "builds.log"), dir, "{}")
		stubBuildRuntimeProbe(t, fakeRuntime{name: "docker", binary: script, available: true})
		_, err := BuildAgentImage(context.Background(), "claude-code", ImageBuildOptions{OverlayImage: "mine/claude:1"})
		require.NoError(t, err)
		assert.Empty(t, probe.engines())
	})
}

// TestHostCLIPin_UnreadableHostCLIKeepsTheUnpinnedImage is TODAY'S behaviour
// for a host whose claude CLI is absent or unreadable: the image is built
// exactly as before pinning existed (unpinned install, pre-pin tag), and the
// gap is announced. Whether that host should instead be refused is an open
// ruling; this pins the status quo until it is made.
func TestHostCLIPin_UnreadableHostCLIKeepsTheUnpinnedImage(t *testing.T) {
	rt := fakeRuntime{name: "docker", available: true}
	for _, tc := range []struct {
		name    string
		version string
		err     error
		cause   string
	}{
		{"absent", "", absentHostCLI, "binary not installed"},
		{"version command failed", "", &engineversion.CommandFailedError{Engine: "claude-code", Err: errors.New("exit 1")}, "version command"},
		{"untaggable version", "2.1.280+build.1", nil, "cannot be written into an image tag"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clidiag.ResetWarnOnce()
			warn := captureWarnings(t)
			withHostCLI(t, tc.version, tc.err)
			c := containerFor(rt, "claude-code", ImageConfig{Base: "ctxloom"})
			assert.NotContains(t, c.image, "-cli")
			sources, _, err := c.containerBuildSources("")
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.NotContains(t, string(sources[0].containerfile), "@anthropic-ai/claude-code@")
			assert.Contains(t, warn.String(), "not pinned")
			assert.Contains(t, warn.String(), tc.cause)
		})
	}
}

// TestHostCLIPin_NoProbeInstalledIsUnpinnedAndSilent: a process with no
// composition root (a unit test) has no host CLI to ask; nothing is pinned
// and nothing is said.
func TestHostCLIPin_NoProbeInstalledIsUnpinnedAndSilent(t *testing.T) {
	t.Cleanup(UseHostCLIVersionProbe(nil))
	clidiag.ResetWarnOnce()
	warn := captureWarnings(t)
	c := containerFor(fakeRuntime{name: "docker", available: true}, "claude-code", ImageConfig{Base: "ctxloom"})
	assert.NotContains(t, c.image, "-cli")
	assert.Empty(t, warn.String())
}

// TestHostCLIPin_EveryComposedBase: ctxloom layers the engine install onto
// whichever base isolation_base names — its own, the project devcontainer, or
// an image ref — so the pin applies on all three alike. The base is the
// user's; the claude in the image is ctxloom's.
func TestHostCLIPin_EveryComposedBase(t *testing.T) {
	withHostCLI(t, "2.1.280", nil)
	rt := fakeRuntime{name: "docker", available: true}
	devRoot := t.TempDir()
	writeDevcontainer(t, devRoot, `{"image": "mcr.microsoft.com/devcontainers/base:ubuntu"}`)
	for name, img := range map[string]ImageConfig{
		"ctxloom":      {Base: "ctxloom"},
		"devcontainer": {Base: "devcontainer", AppRoot: devRoot},
		"image ref":    {Base: "registry.example/team/base:1"},
	} {
		t.Run(name, func(t *testing.T) {
			c := containerFor(rt, "claude-code", img)
			assert.Contains(t, c.image, "-cli2.1.280-")
			sources, _, err := c.containerBuildSources("")
			require.NoError(t, err)
			require.Len(t, sources, 1)
			assert.Contains(t, string(sources[0].containerfile), "@anthropic-ai/claude-code@2.1.280 ")
		})
	}
}
