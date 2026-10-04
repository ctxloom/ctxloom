package cli

import (
	"bytes"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// TestContainerBuildOptions_ExplicitBaseImageDoesNotInheritConfigBase pins
// that isolation.BuildAgentImage rejects BaseImage+Base as mutually
// exclusive, so inheriting a project's isolation_base while --base-image is set
// would make `container build --base-image X` hard-fail on every project that
// configures a base — a flag the user did pass, defeated by a config default
// they did not.
func TestContainerBuildOptions_ExplicitBaseImageDoesNotInheritConfigBase(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{IsolationBase: "devcontainer"})

	opts := containerBuildOptions(
		containerBuildFlagValues{BaseImage: "ghcr.io/example/agent:1"},
		cfg, "claude-code", io.Discard)

	assert.Equal(t, "ghcr.io/example/agent:1", opts.BaseImage, "the flag the user passed must survive")
	assert.Empty(t, opts.Base, "a config isolation_base must not be inherited alongside --base-image")
}

// TestContainerBuildOptions_ConfigBaseAppliesWithoutAFlag is the negative
// control for the guard above: with neither flag, the project's isolation_base
// still applies, so the explicit build and the on-the-fly build resolve the
// same base.
func TestContainerBuildOptions_ConfigBaseAppliesWithoutAFlag(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{IsolationBase: "devcontainer"})

	opts := containerBuildOptions(containerBuildFlagValues{}, cfg, "claude-code", io.Discard)

	assert.Equal(t, "devcontainer", opts.Base)
	assert.Empty(t, opts.BaseImage)
}

// TestContainerBuildOptions_FlagsBeatConfig pins flag-over-config precedence:
// --base overrides isolation_base for this build.
func TestContainerBuildOptions_FlagsBeatConfig(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		IsolationBase:                "devcontainer",
		IsolationEngines:             []string{"mock"},
		IsolationDevcontainerService: "app",
	})

	opts := containerBuildOptions(containerBuildFlagValues{
		Base:                "ctxloom",
		DevcontainerService: "flagged",
		Engines:             []string{"claude-code"},
		KeepCache:           true,
		Runtime:             "podman",
	}, cfg, "claude-code", io.Discard)

	assert.Equal(t, "ctxloom", opts.Base)
	assert.Equal(t, "flagged", opts.DevcontainerService)
	assert.Equal(t, []string{"claude-code"}, opts.Engines)
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
	assert.Empty(t, opts.Base)
	assert.Empty(t, opts.AppRoot)
	assert.Nil(t, opts.Engines)
}

// TestContainerBuildOptions_ConfiguredIsolationImageWarnsThatTheBuildIsUnused
// pins that an isolation_images entry is run AS-IS and never built, so a
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
		IsolationImages: map[string]string{"mock": "ghcr.io/example/other:1"},
	})

	var warn bytes.Buffer
	containerBuildOptions(containerBuildFlagValues{}, cfg, "claude-code", &warn)

	assert.Empty(t, warn.String(), "a pinned image for a DIFFERENT backend is not this build's problem")
}
