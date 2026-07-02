package isolation

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// withFakeSelfExe points resolveSelfExe at a dummy static-binary stand-in so
// ensureImage's build path runs hermetically (no ELF inspection of the real
// test binary, whose linkage is toolchain-dependent).
func withFakeSelfExe(t *testing.T) {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ctxloom")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/true\n"), 0o755))
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveSelfExe = orig })
}

// TestEnsureImage_PresentIsNoop: `<binary> image inspect` succeeding (binary
// "true") short-circuits — no build attempted, no error.
func TestEnsureImage_PresentIsNoop(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "kiro")
	assert.NoError(t, c.ensureImage(context.Background()))
}

// TestEnsureImage_AbsentWithoutRecipeDegrades: an absent image on a profile with
// no embedded Containerfile (the default profile / explicit-image path) errors so
// the caller degrades — exactly the pre-build-support behaviour.
func TestEnsureImage_AbsentWithoutRecipeDegrades(t *testing.T) {
	c := NewContainer(fakeRuntime{name: "docker", binary: "false", available: true}, "img")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not present")
	assert.Contains(t, err.Error(), "no local build recipe")
}

// TestEnsureImage_UserImageIsNeverBuilt: a user-provided image override (config
// isolation_images) is run AS-IS — an absent override degrades without any
// build attempt, even for a backend that IS locally buildable.
func TestEnsureImage_UserImageIsNeverBuilt(t *testing.T) {
	c := containerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "kiro", ImageConfig{Image: "my-registry/my-kiro:v2"})
	assert.Equal(t, "my-registry/my-kiro:v2", c.image)
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no local build recipe")
}

// TestBuildSources_Precedence pins the source order: an explicit base override
// wins outright; else the official client image overlay leads and the embedded
// install Containerfile follows; a profile with neither yields nothing.
func TestBuildSources_Precedence(t *testing.T) {
	// Synthetic profile with BOTH sources (no current profile ships an official
	// image — claude's documented ref does not resolve publicly).
	both := containerProfile{officialImage: "vendor/client:latest", containerfile: []byte("FROM x\n"), validate: "client --version"}
	srcs := buildSources(both, "", "")
	require.Len(t, srcs, 2)
	assert.Contains(t, srcs[0].desc, "official client image vendor/client:latest")
	assert.Nil(t, srcs[0].base, "an overlay is single-stage")
	assert.Contains(t, srcs[1].desc, "install Containerfile")
	require.NotNil(t, srcs[1].base, "the install recipe layers onto the shared base stage")
	assert.NotEmpty(t, srcs[1].base.containerfile, "the default base is the embedded Containerfile")

	override := buildSources(both, "my-base:latest", "")
	require.Len(t, override, 1, "an explicit base override wins outright")
	assert.Contains(t, override[0].desc, "my-base:latest")
	assert.Nil(t, override[0].base)

	// A user base Containerfile LEADS (their environment, our agent layers),
	// with the official overlay and the default-base recipe as fallbacks.
	userBase := buildSources(both, "", "/proj/Containerfile.base")
	require.Len(t, userBase, 3)
	assert.Contains(t, userBase[0].desc, "user base Containerfile /proj/Containerfile.base")
	require.NotNil(t, userBase[0].base)
	assert.Equal(t, "/proj/Containerfile.base", userBase[0].base.path)
	assert.Contains(t, userBase[1].desc, "official client image")
	assert.Contains(t, userBase[2].desc, "install Containerfile")

	for _, backend := range []string{"claude-code", "kiro"} {
		got := buildSources(containerProfileFor(backend), "", "")
		require.Len(t, got, 1, "backend %q builds via its install recipe", backend)
		assert.Contains(t, got[0].desc, "install Containerfile")
		require.NotNil(t, got[0].base, "backend %q layers onto the shared base", backend)
	}

	assert.Empty(t, buildSources(containerProfileFor("codex"), "", ""), "no recipe for unprofiled backends")
}

// TestOverlayContainerfile pins the generated overlay: the base FROM, the
// ENTRYPOINT reset (the isolation runtime supplies the argv), the client
// validate gate, the ctxloom layer, and the in-image ctxloom gate (a
// glibc-linked daily build must fail the BUILD on an incompatible base).
func TestOverlayContainerfile(t *testing.T) {
	cf := string(overlayContainerfile("ghcr.io/anthropics/claude-code:latest", "claude --version"))
	assert.Contains(t, cf, "FROM ghcr.io/anthropics/claude-code:latest\n")
	assert.Contains(t, cf, "ENTRYPOINT []\n")
	assert.Contains(t, cf, "RUN claude --version\n")
	assert.Contains(t, cf, "COPY ctxloom /usr/local/bin/ctxloom\n")
	assert.Contains(t, cf, "RUN /usr/local/bin/ctxloom version\n")

	noValidate := string(overlayContainerfile("base:1", ""))
	assert.NotContains(t, noValidate, "RUN base", "no client validate command → no client RUN gate")
	assert.Contains(t, noValidate, "RUN /usr/local/bin/ctxloom version\n", "the ctxloom gate always runs")
}

// TestEnsureImage_BuildFailureDegrades: an absent image WITH a recipe attempts a
// local build; a failing runtime (binary "false") surfaces a build error so the
// caller degrades — the build is best-effort, never a blocker.
func TestEnsureImage_BuildFailureDegrades(t *testing.T) {
	withFakeSelfExe(t)
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "kiro")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "local build of container image")
}

// TestEnsureImage_UnbuildableBinaryDegrades: an absent image with a recipe but a
// running binary that cannot serve in-container (resolveSelfExe errors) degrades
// with a diagnostic pointing at the ahead-of-time recipes.
func TestEnsureImage_UnbuildableBinaryDegrades(t *testing.T) {
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { resolveSelfExe = orig })

	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "kiro")
	err := c.ensureImage(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be built from this binary")
}

// TestTailLines bounds the failure diagnostics to the last n lines.
func TestTailLines(t *testing.T) {
	assert.Equal(t, "b\nc", tailLines("a\nb\nc\n", 2))
	assert.Equal(t, "a\nb\nc", tailLines("a\nb\nc", 5), "short input passes through whole")
}
