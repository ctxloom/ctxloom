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
// client validate gate, the ctxloom + companions + entrypoint layers, the
// ctxloom entrypoint replacing the base's (identity remap; the runtime
// supplies the argv), the version label, and the in-image ctxloom gate (a
// glibc-linked daily build must fail the BUILD on an incompatible base).
func TestOverlayContainerfile(t *testing.T) {
	cf := string(overlayContainerfile("ghcr.io/anthropics/claude-code:latest", "claude --version"))
	assert.Contains(t, cf, "FROM ghcr.io/anthropics/claude-code:latest\n")
	assert.Contains(t, cf, "RUN claude --version\n")
	assert.Contains(t, cf, "COPY ctxloom /usr/local/bin/ctxloom\n")
	assert.Contains(t, cf, "RUN /usr/local/bin/ctxloom version\n")
	assert.Contains(t, cf, "COPY companions/ /usr/local/bin/\n", "host-mirrored companions layered in")
	assert.Contains(t, cf, companionGate, "companion ABI gate runs")
	assert.Contains(t, cf, overlayUserLayer, "best-effort ctxloom user + gosu layer")
	assert.Contains(t, cf, "COPY ctxloom-entrypoint /usr/local/bin/ctxloom-entrypoint\n")
	assert.Contains(t, cf, "ENTRYPOINT [\"/usr/local/bin/ctxloom-entrypoint\"]\n", "identity-remap entrypoint replaces the base's")
	assert.Contains(t, cf, "LABEL ctxloom.version=\"${CTXLOOM_VERSION}\"\n", "diagnostic version label baked")
	assert.Contains(t, cf, "LABEL ctxloom.provenance=\"${CTXLOOM_PROVENANCE}\"\n", "staleness digest label baked")

	noValidate := string(overlayContainerfile("base:1", ""))
	assert.NotContains(t, noValidate, "RUN base", "no client validate command → no client RUN gate")
	assert.Contains(t, noValidate, "RUN /usr/local/bin/ctxloom version\n", "the ctxloom gate always runs")
}

// TestImageStale: a present image is stale when its ctxloom.provenance label
// (the digest of the baked ctxloom+companion binaries) mismatches the host's
// current digest — including a MISSING label (an image predating provenance
// rebuilds once). An empty wanted digest (unresolvable host binary) disables
// the check entirely.
func TestImageStale(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   string
		stale  bool
	}{
		{"matching digest", map[string]string{"ctxloom.provenance": "sha-abc"}, "sha-abc", false},
		{"mismatched digest (binaries changed)", map[string]string{"ctxloom.provenance": "sha-old"}, "sha-new", true},
		{"missing label (predates provenance)", map[string]string{}, "sha-new", true},
		{"nil labels (inspect failed)", nil, "sha-new", true},
		{"unknown host digest disables check", map[string]string{"ctxloom.provenance": "sha-old"}, "", false},
		{"version label ignored", map[string]string{"ctxloom.version": "v1.2.3"}, "sha-new", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.stale, imageStale(tt.labels, tt.want))
		})
	}
}

// TestComputeProvenanceDigest_TracksBinaryContent: the digest changes when the
// ctxloom binary content changes, when a companion's content changes, and when
// the present-companion set changes — the three "rebuild the image" triggers —
// and is stable when nothing changed. An unresolvable self-exe yields "".
func TestComputeProvenanceDigest_TracksBinaryContent(t *testing.T) {
	bin := t.TempDir()
	self := filepath.Join(bin, "ctxloom")
	require.NoError(t, os.WriteFile(self, []byte("ctxloom-v1"), 0o755))
	writeCompanion := func(name, content string) {
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte(content), 0o755))
	}
	writeCompanion("taskloom", "taskloom-v1")
	writeCompanion("ltk", "ltk-v1")
	// reprise deliberately absent to start.
	t.Setenv("PATH", bin)

	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return self, nil }
	t.Cleanup(func() { resolveSelfExe = orig })

	base := computeProvenanceDigest()
	require.NotEmpty(t, base)
	assert.Equal(t, base, computeProvenanceDigest(), "stable when nothing changes")

	require.NoError(t, os.WriteFile(self, []byte("ctxloom-v2"), 0o755))
	afterSelf := computeProvenanceDigest()
	assert.NotEqual(t, base, afterSelf, "ctxloom rebuild changes the digest")

	writeCompanion("taskloom", "taskloom-v2")
	afterCompanion := computeProvenanceDigest()
	assert.NotEqual(t, afterSelf, afterCompanion, "companion update changes the digest")

	writeCompanion("reprise", "reprise-v1")
	afterAdd := computeProvenanceDigest()
	assert.NotEqual(t, afterCompanion, afterAdd, "a newly-present companion changes the digest")

	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	assert.Empty(t, computeProvenanceDigest(), "unresolvable self-exe → empty (check disabled)")
}

// TestEnsureImage_UserOwnedOverrideRunsAsIs: a present image with NO local
// build recipe (the isolation_images override case) is never inspected for
// staleness or rebuilt — the user owns its lifecycle.
func TestEnsureImage_UserOwnedOverrideRunsAsIs(t *testing.T) {
	// fakeRuntime binary "true": `true image inspect …` exits 0 → present; the
	// default profile for an image-only override has no build sources.
	c := NewContainer(fakeRuntime{name: "docker", binary: "true", available: true}, "user/own:img")
	assert.NoError(t, c.ensureImage(context.Background()),
		"present + no recipe → run as-is, no staleness gate")
}

// TestStageCompanions_MirrorsPresentSkipsMissing: staging copies every
// companion resolvable on the host PATH into <context>/companions and skips the
// rest — the dir itself always exists so the agent stages' `COPY companions/`
// succeeds even when nothing shipped.
func TestStageCompanions_MirrorsPresentSkipsMissing(t *testing.T) {
	bin := t.TempDir()
	for _, name := range []string{"taskloom", "reprise"} { // ltk deliberately absent
		require.NoError(t, os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"), 0o755))
	}
	t.Setenv("PATH", bin)

	ctxDir := t.TempDir()
	require.NoError(t, stageCompanions(ctxDir))

	assert.FileExists(t, filepath.Join(ctxDir, "companions", "taskloom"))
	assert.FileExists(t, filepath.Join(ctxDir, "companions", "reprise"))
	assert.NoFileExists(t, filepath.Join(ctxDir, "companions", "ltk"), "missing companion skipped, not fatal")

	info, err := os.Stat(filepath.Join(ctxDir, "companions", "taskloom"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&0o111, "staged companion keeps the executable bit")
}

// TestStageCompanions_EmptyPathStillCreatesDir: with no companion on PATH the
// staging still creates the (empty) companions dir the Containerfiles COPY.
func TestStageCompanions_EmptyPathStillCreatesDir(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	ctxDir := t.TempDir()
	require.NoError(t, stageCompanions(ctxDir))
	entries, err := os.ReadDir(filepath.Join(ctxDir, "companions"))
	require.NoError(t, err)
	assert.Empty(t, entries)
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
