package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
)

// TestContainerBuildOptions_ExplicitBaseImageDoesNotInheritConfigBaseContainerfile
// pins U035-F02: isolation.BuildAgentImage rejects BaseImage+BaseContainerfile
// as mutually exclusive, so inheriting a project's
// isolation_base_containerfile while --base-image is set made `container build
// --base-image X` hard-fail on every project that configures a base
// Containerfile — a flag the user did pass, defeated by a config default they
// did not.
func TestContainerBuildOptions_ExplicitBaseImageDoesNotInheritConfigBaseContainerfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationBaseContainerfile: "/proj/.ctxloom/base.Containerfile",
	})

	opts := containerBuildOptions(
		containerBuildFlagValues{BaseImage: "ghcr.io/example/agent:1"},
		cfg, "claude-code", io.Discard)

	assert.Equal(t, "ghcr.io/example/agent:1", opts.BaseImage, "the flag the user passed must survive")
	assert.Empty(t, opts.BaseContainerfile,
		"a config base Containerfile must not be inherited alongside --base-image")
	require.False(t, opts.BaseImage != "" && opts.BaseContainerfile != "",
		"the pair isolation.BuildAgentImage rejects must never be formed from one flag plus config")
}

// TestContainerBuildOptions_ConfigBaseContainerfileAppliesWithoutBaseImage is
// the negative control for the guard above: with no --base-image, the project's
// configured base Containerfile still applies, so the explicit build and the
// on-the-fly build keep resolving the same base.
func TestContainerBuildOptions_ConfigBaseContainerfileAppliesWithoutBaseImage(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationBaseContainerfile: "/proj/.ctxloom/base.Containerfile",
	})

	opts := containerBuildOptions(containerBuildFlagValues{}, cfg, "claude-code", io.Discard)

	assert.Equal(t, "/proj/.ctxloom/base.Containerfile", opts.BaseContainerfile)
	assert.Empty(t, opts.BaseImage)
}

// TestContainerBuildOptions_ExplicitBaseContainerfileBeatsConfig pins the
// pre-existing flag-over-config precedence the extraction must preserve.
func TestContainerBuildOptions_ExplicitBaseContainerfileBeatsConfig(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationBaseContainerfile:   "/proj/.ctxloom/base.Containerfile",
		IsolationEngines:             []string{"codex"},
		IsolationDevcontainerService: "app",
	})

	opts := containerBuildOptions(containerBuildFlagValues{
		BaseContainerfile:   "/tmp/mine.Containerfile",
		DevcontainerService: "flagged",
		Engines:             []string{"claude-code"},
		NoDevcontainerBase:  true,
		KeepCache:           true,
		Runtime:             "podman",
	}, cfg, "claude-code", io.Discard)

	assert.Equal(t, "/tmp/mine.Containerfile", opts.BaseContainerfile)
	assert.Equal(t, "flagged", opts.DevcontainerService)
	assert.Equal(t, []string{"claude-code"}, opts.Engines)
	assert.True(t, opts.NoDevcontainerBase)
	assert.True(t, opts.KeepCache)
	assert.Equal(t, "podman", opts.Runtime)
}

// TestContainerBuildOptions_NilConfigResolvesFromFlagsAlone covers the
// config-unavailable path (GetConfig failed): flags still resolve, nothing
// panics, and no config-derived field is invented.
func TestContainerBuildOptions_NilConfigResolvesFromFlagsAlone(t *testing.T) {
	opts := containerBuildOptions(
		containerBuildFlagValues{BaseImage: "ghcr.io/example/agent:1"},
		nil, "claude-code", io.Discard)

	assert.Equal(t, "ghcr.io/example/agent:1", opts.BaseImage)
	assert.Empty(t, opts.BaseContainerfile)
	assert.Empty(t, opts.AppRoot)
	assert.Nil(t, opts.Engines)
}

// TestContainerBuildOptions_ConfiguredIsolationImageWarnsThatTheBuildIsUnused
// pins U035-F03: an isolation_images entry is run AS-IS and never built, so a
// build for that backend produces an image no run will ever use. Silence there
// is this project's characteristic failure — a success message for work with
// no effect — so the mismatch must be reported.
func TestContainerBuildOptions_ConfiguredIsolationImageWarnsThatTheBuildIsUnused(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationImages: map[string]string{"claude-code": "ghcr.io/example/pinned:9"},
	})

	var warn bytes.Buffer
	containerBuildOptions(containerBuildFlagValues{}, cfg, "claude-code", &warn)

	out := warn.String()
	assert.Contains(t, out, "isolation_images", "the warning must name the config key responsible")
	assert.Contains(t, out, "ghcr.io/example/pinned:9", "and the image that wins over this build")
	assert.Contains(t, out, "never built")
}

// TestContainerBuildOptions_NoIsolationImageIsSilent is the negative control:
// the ordinary project (no pinned image) must not warn.
func TestContainerBuildOptions_NoIsolationImageIsSilent(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationImages: map[string]string{"codex": "ghcr.io/example/other:1"},
	})

	var warn bytes.Buffer
	containerBuildOptions(containerBuildFlagValues{}, cfg, "claude-code", &warn)

	assert.Empty(t, warn.String(), "a pinned image for a DIFFERENT backend is not this build's problem")
}
