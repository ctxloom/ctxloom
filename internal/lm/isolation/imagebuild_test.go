package isolation

import (
	"context"
	"debug/elf"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containerfiles "github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// withFakeSelfExe points resolveSelfExe at a dummy static-binary stand-in so
// ensureImage's build path runs hermetically (no ELF inspection of the real
// test binary, whose linkage is toolchain-dependent). Returns the stand-in's
// path for callers that pass selfExe explicitly.
func withFakeSelfExe(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "ctxloom")
	require.NoError(t, os.WriteFile(exe, []byte("#!/bin/true\n"), 0o755))
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { resolveSelfExe = orig })
	return exe
}

// writeFakeRuntimeScript writes a container-runtime CLI stand-in at path:
// `build` appends its argv to logFile and marks the -t image present (a
// marker file under markerDir, keyed by image name); `image inspect` reports
// presence by that marker and answers --format with labelsJSON. It makes the
// ensure/build flow observable without a daemon. The build's short sleep
// widens the concurrency window so racing callers demonstrably overlap.
func writeFakeRuntimeScript(t *testing.T, path, logFile, markerDir, labelsJSON string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
# Tests empty the process PATH (companion hermeticity); pin one for tr/touch/sleep.
PATH=/usr/bin:/bin
name=$(printf '%%s' "$3" | tr '/:' '__')
case "$1" in
build)
    echo "$@" >> %q
    sleep 0.1
    touch %q/"$name"
    ;;
image)
    [ -f %q/"$name" ] || exit 1
    if [ "$4" = "--format" ]; then printf '%%s' %q; fi
    ;;
esac
exit 0
`, logFile, markerDir, markerDir, labelsJSON)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

// buildInvocations returns the fake runtime's logged `build` argv lines
// (empty when no build ran).
func buildInvocations(t *testing.T, logFile string) []string {
	t.Helper()
	out, err := os.ReadFile(logFile)
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

// TestEnsureImage_PresentIsNoop: `<binary> image inspect` succeeding (binary
// "true") short-circuits — no build attempted, no error.
func TestEnsureImage_PresentIsNoop(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "kiro")
	assert.NoError(t, c.ensureImage(context.Background()))
}

// TestEnsureImage_AbsentWithoutRecipeDegrades: an absent image on a spec with
// no embedded Containerfile (the default spec / explicit-image path) errors so
// the caller degrades — exactly the pre-build-support behaviour.
func TestEnsureImage_AbsentWithoutRecipeDegrades(t *testing.T) {
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "false", available: true}, "mock").WithImage("img")
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

// TestBuildSources_NonComposableHasNoRecipe pins the NON-COMPOSABLE shape (no
// engineInstall — an unknown/unmapped backend, e.g. engineContainerSpecFor's
// `default` arm): there is no local-build recipe at all, so buildSources
// yields nothing regardless of the base-Containerfile/devcontainer options —
// UNLESS an explicit base-image override is given, which still wins outright
// (the caller asserts the client already lives there; no spec lookup is
// needed to overlay onto it).
//
// A prior fix deleted the LEGACY officialImage/user-Containerfile/embedded-
// Containerfile fallback this spec shape used to fall through to: no
// spec, registered or hypothetical, ever populated those fields (rg over
// the whole repo found zero assignments), so the fallback's three append
// blocks could never produce a source — this test used to exercise them only
// via a synthetic engineContainerSpec{officialImage: ..., containerfile: ...}
// literal built by nothing in production.
func TestBuildSources_NonComposableHasNoRecipe(t *testing.T) {
	p := engineContainerSpecFor("no-such-engine")
	require.Nil(t, p.engineInstall, "precondition: an unmapped backend is the non-composable default spec")

	assert.Empty(t, buildSources(p, buildSourcesOptions{}), "no recipe for an unmapped/non-composable backend")
	assert.Empty(t, buildSources(p, buildSourcesOptions{baseContainerfile: "/proj/Containerfile.base"}),
		"a user base Containerfile alone still yields nothing without an engineInstall fragment to layer onto it")

	override := buildSources(p, buildSourcesOptions{baseOverride: "my-base:latest"})
	require.Len(t, override, 1, "an explicit base override wins outright, even for a non-composable spec")
	assert.Contains(t, override[0].desc, "my-base:latest")
	assert.Nil(t, override[0].base)
}

// TestBuildSources_Composable pins the COMPOSABLE spec shape
// (claude-code/codex/kiro/opencode — engineInstall != nil): the SAME
// generated multi-engine Containerfile builds onto each candidate base in
// precedence order (explicit user base > auto-detected devcontainer >
// embedded default), and an explicit base-image override still wins outright
// exactly like the legacy shape.
func TestBuildSources_Composable(t *testing.T) {
	for _, backend := range []string{"claude-code", "codex", "kiro", "opencode"} {
		p := engineContainerSpecFor(backend)
		require.NotNil(t, p.engineInstall, "backend %q must be composable", backend)

		got := buildSources(p, buildSourcesOptions{})
		require.Len(t, got, 1, "backend %q: default-base only", backend)
		assert.Contains(t, got[0].desc, "composed agent stage")
		assert.Contains(t, got[0].desc, "embedded default base")
		require.NotNil(t, got[0].base)

		override := buildSources(p, buildSourcesOptions{baseOverride: "my-base:latest"})
		require.Len(t, override, 1, "an explicit base-image override wins outright")
		assert.Contains(t, override[0].desc, "my-base:latest")
		assert.Nil(t, override[0].base)

		userBase := buildSources(p, buildSourcesOptions{baseContainerfile: "/proj/Containerfile.base"})
		require.Len(t, userBase, 2, "user base Containerfile leads, default base falls back")
		assert.Contains(t, userBase[0].desc, "user base Containerfile /proj/Containerfile.base")
		assert.Contains(t, userBase[1].desc, "embedded default base")

		dev := &baseStage{desc: "test devcontainer", containerfile: []byte("FROM debian:13\n"), kind: baseStageKindDevcontainer}
		withDev := buildSources(p, buildSourcesOptions{devBase: dev})
		require.Len(t, withDev, 2, "auto-detected devcontainer base, default base falls back")
		assert.Contains(t, withDev[0].desc, "auto-detected project devcontainer")
		assert.Contains(t, withDev[1].desc, "embedded default base")

		all := buildSources(p, buildSourcesOptions{baseContainerfile: "/proj/Containerfile.base", devBase: dev})
		require.Len(t, all, 3, "explicit user base beats the auto-detected devcontainer, default base still falls back")
		assert.Contains(t, all[0].desc, "user base Containerfile")
		assert.Contains(t, all[1].desc, "auto-detected project devcontainer")
		assert.Contains(t, all[2].desc, "embedded default base")
	}
}

// TestBuildSources_MockIsComposable pins that mock's own spec now has a
// local-build recipe: `ctxloom container build mock` used to fail outright
// ("no local build recipe for this engine") because engineContainerSpecFor fell
// through to the non-composable default for any unrecognized/unmapped
// name, mock included. mockInstallFragment being non-nil is what flips
// buildSources from empty to composableBuildSources' output — this pins the
// OUTCOME (buildSources itself), not just the fragment's non-nilness, so a
// future change that sets engineInstall but breaks buildSources' composable
// branch for it would still be caught here.
func TestBuildSources_MockIsComposable(t *testing.T) {
	p := engineContainerSpecFor("mock")
	require.NotNil(t, p.engineInstall, "precondition: mock is composable")

	got := buildSources(p, buildSourcesOptions{})
	require.NotEmpty(t, got, "mock must have a local-build recipe now (previously nil/empty — 'no local build recipe for this engine')")
	assert.Contains(t, got[0].desc, "composed agent stage")
}

// TestComposeAgentContainerfile_EngineOrderAndGates pins composeAgentContainerfile's
// shape: one RUN fragment per selected engine in the given order, plus the
// shared identity/entrypoint/companion scaffolding every composed image needs.
func TestComposeAgentContainerfile_EngineOrderAndGates(t *testing.T) {
	cf := string(composeAgentContainerfile([]string{"claude-code", "kiro"}))
	assert.Contains(t, cf, "ARG BASE_IMAGE=ctxloom-agent-base:latest\n")
	assert.Contains(t, cf, "FROM ${BASE_IMAGE}\n")
	assert.Contains(t, cf, baseContractLayer, "best-effort tool layer for an arbitrary base")
	assert.Contains(t, cf, overlayUserLayer)
	assert.Contains(t, cf, overlayUserGate)
	assert.Contains(t, cf, "claude --version", "claude's engine fragment is included")
	assert.Contains(t, cf, "kiro-cli --version", "kiro's engine fragment is included")
	assert.Less(t, strings.Index(cf, "claude --version"), strings.Index(cf, "kiro-cli --version"), "engines compose in the GIVEN order")
	assert.Contains(t, cf, "COPY ctxloom /usr/local/bin/ctxloom\n")
	assert.Contains(t, cf, "COPY companions/ /usr/local/bin/\n")
	assert.Contains(t, cf, "RUN /usr/local/bin/ctxloom version\n")
	assert.Contains(t, cf, companionGate)

	// A backend with no known fragment is silently skipped (resolveEngines
	// already warns about it before this is ever called with such a name).
	single := string(composeAgentContainerfile([]string{"claude-code"}))
	assert.NotContains(t, single, "kiro-cli", "only the requested engine's fragment is baked")
}

// TestComposeAgentContainerfile_CodexIncludesACPAdapter pins the real gap
// this ACP-transport generalization found: codexInstallFragment previously
// installed the `codex` client only, never the `codex-acp` adapter, so a
// containerized codex agent's structured chat (internal/codex/chat.go's
// Chat(), gated by req.Runtime != agent.RuntimeContainer, trusts the IMAGE
// to already carry the adapter) silently had no adapter to spawn. Mirrors
// claude's fragment, which has always installed BOTH claude and
// claude-code-acp in one npm line.
func TestComposeAgentContainerfile_CodexIncludesACPAdapter(t *testing.T) {
	cf := string(composeAgentContainerfile([]string{"codex"}))
	assert.Contains(t, cf, "codex-acp", "the codex-acp adapter must be installed alongside codex itself")
	assert.Contains(t, cf, "command -v codex-acp", "a hard validate gate must prove the adapter actually landed, not just be requested")
}

// TestResolveEngines pins the default/override/unknown-filtering behaviour:
// empty config = every known fragment; a configured subset is reordered to
// composableEngines()'s DETERMINISTIC order (never the caller's order); an
// unknown/non-composable name is dropped, never silently promoted to "use
// everything".
func TestResolveEngines(t *testing.T) {
	assert.Equal(t, composableEngines(), resolveEngines(nil), "empty config = every known fragment")
	assert.Equal(t, []string{"claude-code", "kiro"}, resolveEngines([]string{"kiro", "claude-code"}),
		"reordered to composableEngines() order regardless of input order")
	assert.Equal(t, []string{"claude-code"}, resolveEngines([]string{"claude-code", "not-a-real-engine"}),
		"an unknown engine is dropped, not promoted to \"use everything\"")
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
	assert.Contains(t, cf, overlayUserGate, "the identity machinery is VERIFIED after the attempts, not best-effort")
	assert.Contains(t, cf, "COPY ctxloom-entrypoint /usr/local/bin/ctxloom-entrypoint\n")
	assert.Contains(t, cf, "ENTRYPOINT [\"/usr/local/bin/ctxloom-entrypoint\"]\n", "identity-remap entrypoint replaces the base's")
	assert.Contains(t, cf, "LABEL ctxloom.version=\"${CTXLOOM_VERSION}\"\n", "diagnostic version label baked")
	assert.Contains(t, cf, "LABEL ctxloom.provenance=\"${CTXLOOM_PROVENANCE}\"\n", "staleness digest label baked")

	noValidate := string(overlayContainerfile("base:1", ""))
	assert.NotContains(t, noValidate, "RUN base", "no client validate command → no client RUN gate")
	assert.Contains(t, noValidate, "RUN /usr/local/bin/ctxloom version\n", "the ctxloom gate always runs")
}

// TestBuildSources_OverrideWithoutValidateWarns pins that
// the overlay Containerfile emits its client-validation `RUN` only
// when the spec HAS a validate command, and the default (unmapped)
// spec has none. `ctxloom container build <unmapped> --base-image X`
// therefore shipped an agent image whose engine was never proven to exist —
// it builds, tags, passes every ctxloom/companion gate, and fails at run time
// with the engine binary simply absent. The rendering is correct (there is no
// command to run); the silence about it was not.
func TestBuildSources_OverrideWithoutValidateWarns(t *testing.T) {
	p := engineContainerSpecFor("no-such-engine")
	require.Empty(t, p.validate, "precondition: the default spec has no client validate command")

	buf := captureWarnings(t)
	sources := buildSources(p, buildSourcesOptions{baseOverride: "my-base:latest"})
	require.Len(t, sources, 1, "the override still wins outright — this must stay a warning, not a refusal")
	assert.NotContains(t, string(sources[0].containerfile), "RUN \n", "no empty validate RUN is rendered")

	warning := buf.String()
	assert.Contains(t, warning, "my-base:latest")
	assert.Contains(t, warning, "client-validation", "the warning names the missing client-validation gate")
}

// TestBuildSources_OverrideWithValidateIsSilent: a mapped backend's override
// DOES get its validate gate, so it must not draw the missing-client-validation warning.
func TestBuildSources_OverrideWithValidateIsSilent(t *testing.T) {
	buf := captureWarnings(t)
	sources := buildSources(engineContainerSpecFor("claude-code"), buildSourcesOptions{baseOverride: "my-base:latest"})
	require.Len(t, sources, 1)
	assert.Contains(t, string(sources[0].containerfile), "RUN claude --version\n")
	assert.Empty(t, buf.String(), "a spec that CAN validate its client warns about nothing")
}

// TestOverlayUserGate_FailsTheBuildWithAFixIt: a base that cannot grow the
// identity machinery (the ctxloom user, and setpriv or gosu+usermod+groupmod)
// must FAIL the build with a fix-it — the old all-`|| true` layer shipped an
// image whose engine then ran as root behind a buried in-container warning,
// invisible to the launch-failure gate. The install ATTEMPTS stay best-effort
// (arbitrary bases bring arbitrary package managers); only the VERIFICATION
// is hard.
func TestOverlayUserGate_FailsTheBuildWithAFixIt(t *testing.T) {
	assert.Contains(t, overlayUserGate, "exit 1", "an unmet contract fails the build")
	assert.NotContains(t, overlayUserGate, "|| true", "the verification is never swallowed")
	assert.Contains(t, overlayUserGate, "id ctxloom", "the ctxloom user must exist")
	assert.Contains(t, overlayUserGate, "setpriv", "setpriv alone suffices (numeric ids, remap-immune)")
	assert.Contains(t, overlayUserGate, "usermod", "gosu needs a working remap, so it requires usermod/groupmod")
	// Both failure messages carry a fix-it naming what to install.
	assert.Contains(t, overlayUserGate, "install", "the build failure tells the user how to fix the base")
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
	// default spec for an image-only override has no build sources.
	c := NewContainerFor(fakeRuntime{name: "docker", binary: "true", available: true}, "mock").WithImage("user/own:img")
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
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm(), "staged companion is 0755 exactly, umask notwithstanding")
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

// TestEnsureImage_StaleUnbuildableFromThisBinary_RecordsFinding pins that
// a STALE (present, provenance mismatch) image that cannot even be REBUILT
// because resolveSelfExe fails (e.g. this dev host cannot serve in-container)
// used to `return nil` here with NO warning and NO finding at all — silently
// degrading the isolation boundary to a stale, possibly pre-entrypoint
// (root-running) image. It must now record the SAME fatal ClassIsolation
// finding the parallel "rebuild attempted and failed" branch already
// produces for the identical outcome.
func TestEnsureImage_StaleUnbuildableFromThisBinary_RecordsFinding(t *testing.T) {
	resetStrictness(t)
	forceProvenance(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	writeInspectOKBuildFailScript(t, script, `{"ctxloom.provenance":"stale-nomatch"}`)

	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { resolveSelfExe = orig })

	c := Container{
		runtime:    fakeRuntime{name: "docker", binary: script, available: true},
		image:      "ctxloom-agent-stale-unbuildable-test:latest",
		engineSpec: engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")},
	}
	require.NoError(t, c.ensureImage(context.Background()),
		"the stale image still launches — unbuildable-from-this-binary must never take the container axis down")

	findings := strictness.All()
	require.Len(t, findings, 1, "a stale image that cannot even be rebuilt from this binary must record exactly one fatal finding")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "STALE")
	assert.Contains(t, findings[0].FixIt, "--degraded")
}

// TestComposableBuildSources_EmptyEnginesFailsLoud pins that
// isolation_engines resolving to NO known engine (every configured name
// unknown or non-composable) used to silently produce a composed
// Containerfile with zero engine-install layers — it builds, tags, passes
// every gate, and the run fails with the engine binary simply absent. It
// must now record a fatal ClassIsolation finding and return no build
// sources at all, rather than a green recipe for an empty image.
func TestComposableBuildSources_EmptyEnginesFailsLoud(t *testing.T) {
	resetStrictness(t)
	p := engineContainerSpec{engineInstall: []byte("RUN something\n")}
	sources := composableBuildSources(p, buildSourcesOptions{engines: []string{"totally-unknown-engine"}})
	assert.Nil(t, sources, "no build source at all for a resolved-empty engine set")

	findings := strictness.All()
	require.Len(t, findings, 1)
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].Message, "no known engine")
}

// TestTailLines bounds the failure diagnostics to the last n lines.
func TestTailLines(t *testing.T) {
	assert.Equal(t, "b\nc", tailLines("a\nb\nc\n", 2))
	assert.Equal(t, "a\nb\nc", tailLines("a\nb\nc", 5), "short input passes through whole")
}

// TestEnsureImage_ParallelCallersShareOneBuild: a fan-out drives N members
// through ensureImage for the SAME absent tag concurrently — exactly one
// FLIGHT must run (2 build invocations: the shared base stage, then the agent
// stage FROM it — buildFromSource's shape for any source with a non-nil
// base, which every composable spec carries), every caller sharing its
// outcome (pre-dedup, each member raced its own build and a mid-build untag
// could flake another's post-build recheck). A later, non-overlapping ensure
// re-checks presence instead of rebuilding, and a DIFFERENT tag still builds
// independently — flights are per-tag.
func TestEnsureImage_ParallelCallersShareOneBuild(t *testing.T) {
	withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir()) // no companions on PATH: staging warns + skips

	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}
	// The fake runtime reports the just-built image's provenance label as
	// current, so a caller landing after the flight re-checks cheaply instead
	// of rebuilding. This spec is COMPOSABLE (engineInstall != nil), so its
	// real provenance comes from composedIdentity (content+engine-keyed),
	// never the legacy HostProvenanceDigest — computed here with the same
	// nil devBase/nil engines ensureImage itself resolves for this Container
	// (appRoot == "" short-circuits devcontainer auto-detection to nil).
	_, provenance, ok := composedIdentity(spec, "", nil, nil)
	require.True(t, ok, "precondition: a composable spec always resolves a provenance")
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, provenance)
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, labels)

	c := Container{
		runtime:    fakeRuntime{name: "docker", binary: script, available: true},
		image:      "ctxloom-agent-dedup-test:latest",
		engineSpec: spec,
	}

	const n = 8
	var (
		start = make(chan struct{})
		wg    sync.WaitGroup
		errs  [n]error
	)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			errs[i] = c.ensureImage(context.Background())
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "caller %d shares the flight outcome", i)
	}
	assert.Len(t, buildInvocations(t, logFile), 2, "one flight (base + agent stage) per tag, not one per fan-out member")

	require.NoError(t, c.ensureImage(context.Background()))
	assert.Len(t, buildInvocations(t, logFile), 2, "a present, current image never rebuilds")

	c2 := c
	c2.image = "ctxloom-agent-dedup-test-2:latest"
	require.NoError(t, c2.ensureImage(context.Background()))
	assert.Len(t, buildInvocations(t, logFile), 4, "a different tag is its own flight and builds its own base+agent stage pair")
}

// TestBuildFromSource_BaseTagPerConfigContent: the shared base stage's tag is
// keyed by the base Containerfile CONTENT — two configs build two tags (no
// cross-session contamination through a fixed :latest), the agent stage FROMs
// exactly the tag its own flight built (--build-arg BASE_IMAGE), and identical
// content re-lands on the identical tag so the layer cache still shares.
func TestBuildFromSource_BaseTagPerConfigContent(t *testing.T) {
	selfExe := withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, "{}")
	rt := fakeRuntime{name: "docker", binary: script, available: true}

	build := func(baseContent string) {
		t.Helper()
		src := buildSource{
			desc:          "test recipe",
			containerfile: []byte("ARG BASE_IMAGE\nFROM ${BASE_IMAGE}\n"),
			base:          &baseStage{desc: "test base", containerfile: []byte(baseContent)},
		}
		require.NoError(t, buildFromSource(context.Background(), rt, "ctxloom-agent-basetag-test:latest", src, selfExe, "", false, nil))
	}
	build("FROM debian:13\n")
	build("FROM alpine:3\n")
	build("FROM debian:13\n")

	// Each buildFromSource logs a base build then an agent build.
	lines := buildInvocations(t, logFile)
	require.Len(t, lines, 6)
	var baseTags, fromArgs []string
	for i, line := range lines {
		fields := strings.Fields(line)
		require.Greater(t, len(fields), 2, "line %d: %s", i, line)
		if i%2 == 0 {
			baseTags = append(baseTags, fields[2])
			continue
		}
		require.Contains(t, line, "BASE_IMAGE=", "agent build line %d wires the base tag", i)
		arg := line[strings.Index(line, "BASE_IMAGE=")+len("BASE_IMAGE="):]
		fromArgs = append(fromArgs, strings.Fields(arg)[0])
	}
	for i, tag := range baseTags {
		assert.Regexp(t, `^ctxloom-agent-base:[0-9a-f]{12}$`, tag)
		assert.Equal(t, tag, fromArgs[i], "the agent stage FROMs the tag its own flight built")
	}
	assert.NotEqual(t, baseTags[0], baseTags[1], "different base content → different tag")
	assert.Equal(t, baseTags[0], baseTags[2], "identical base content → identical tag")

	// A user-provided base Containerfile is keyed by the same content hash —
	// the config FORM (file vs embedded) doesn't fragment the tag space — and
	// a missing file still errors.
	userFile := filepath.Join(t.TempDir(), "Containerfile.base")
	require.NoError(t, os.WriteFile(userFile, []byte("FROM debian:13\n"), 0o644))
	tag, err := buildBaseImage(context.Background(), rt, userBaseStage(userFile), false, nil)
	require.NoError(t, err)
	assert.Equal(t, baseImageTagFor([]byte("FROM debian:13\n")), tag)
	_, err = buildBaseImage(context.Background(), rt, userBaseStage(filepath.Join(t.TempDir(), "missing")), false, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "base containerfile")
}

// TestCombineProvenance_CoversBaseConfig: the ONE existing staleness gate (the
// ctxloom.provenance label) also covers the base Containerfile config — same
// binaries + different base config → different digest, so the agent image
// rebuilds onto the right base. An unknown half yields "" (the check disables
// rather than force a wrong rebuild), and the suffix is the base tag's content
// hash, naming the base generation the image rode on.
func TestCombineProvenance_CoversBaseConfig(t *testing.T) {
	deflt := combineProvenance("bin-digest", "")
	assert.Equal(t, "bin-digest-"+baseContentHash(containerfiles.Base()), deflt)

	userFile := filepath.Join(t.TempDir(), "Containerfile.base")
	require.NoError(t, os.WriteFile(userFile, []byte("FROM debian:13\n"), 0o644))
	user := combineProvenance("bin-digest", userFile)
	assert.Equal(t, "bin-digest-"+baseContentHash([]byte("FROM debian:13\n")), user)
	assert.NotEqual(t, deflt, user, "a different base config is a different provenance")

	assert.Empty(t, combineProvenance("", ""), "unknown binaries digest disables the check")
	assert.Empty(t, combineProvenance("bin-digest", filepath.Join(t.TempDir(), "missing")),
		"unreadable base config disables the check")
}

// TestCopyExecutable_Forces0755: staged binaries land at 0755 EXACTLY, even
// over a pre-narrowed file — the explicit chmod is what defeats a restrictive
// umask, whose narrowed mode (0700) passes the root-run in-image build gates
// but fails exec for the dropped ctxloom-user at runtime.
func TestCopyExecutable_Forces0755(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	require.NoError(t, os.WriteFile(src, []byte("#!/bin/sh\n"), 0o755))

	dst := filepath.Join(dir, "dst")
	require.NoError(t, os.WriteFile(dst, nil, 0o600)) // narrowed, as under umask 077
	require.NoError(t, copyExecutable(src, dst))

	info, err := os.Stat(dst)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), info.Mode().Perm())
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\n", string(got))
}

// TestCompanionGate_DerivedFromCompanionList: the gate's shell loop iterates
// exactly companionBinaries — adding a companion to the slice can never
// silently miss the in-image ABI gate (a hard-coded literal could drift).
func TestCompanionGate_DerivedFromCompanionList(t *testing.T) {
	assert.Equal(t, companionGateFor(companionBinaries), companionGate)
	assert.Contains(t, companionGate, "for b in "+strings.Join(companionBinaries, " ")+"; do")

	synthetic := companionGateFor([]string{"alpha", "beta"})
	assert.Contains(t, synthetic, "for b in alpha beta; do")
	assert.NotContains(t, synthetic, "taskloom", "no hard-coded companion survives the derivation")
}

// writeInspectOKBuildFailScript writes a runtime stand-in whose `image inspect`
// always succeeds (image PRESENT, returning labelsJSON on --format) but whose
// `build` always fails — the stale-image / failed-rebuild shape.
func writeInspectOKBuildFailScript(t *testing.T, path, labelsJSON string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
PATH=/usr/bin:/bin
case "$1" in
build) exit 1 ;;
image) if [ "$4" = "--format" ]; then printf '%%s' %q; fi; exit 0 ;;
esac
exit 0
`, labelsJSON)
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

// writeAbsentBuildFailScript writes a runtime stand-in whose `image inspect` and
// `build` both fail — an ABSENT image whose local build cannot succeed.
func writeAbsentBuildFailScript(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
PATH=/usr/bin:/bin
case "$1" in
build) exit 1 ;;
image) exit 1 ;;
esac
exit 0
`
	require.NoError(t, os.WriteFile(path, []byte(script), 0o755))
}

// forceProvenance reseeds the process-global provenance cache (HostProvenance-
// Digest memoizes once per process) so the staleness gate is LIVE and
// deterministic regardless of test order. Requires resolveSelfExe to already
// point at a readable stand-in (withFakeSelfExe), which makes the digest
// non-empty. The cleanup clears it so the next caller recomputes against the
// restored (real) binary.
func forceProvenance(t *testing.T) {
	t.Helper()
	provenanceOnce = sync.Once{}
	provenanceCached = ""
	require.NotEmpty(t, HostProvenanceDigest(""), "the fake selfExe must yield a non-empty provenance digest")
	t.Cleanup(func() {
		provenanceOnce = sync.Once{}
		provenanceCached = ""
	})
}

// clearProvenanceCache empties the process-global provenance memo WITHOUT
// seeding it, so the next computation runs against whatever resolveSelfExe is
// installed at that moment. forceProvenance's inverse: it exists because
// seeding first and breaking resolveSelfExe afterwards builds a state
// production can never reach (production resolves the digest lazily, through
// the SAME seam the build path later uses), which is how an unreachable
// branch can look covered.
func clearProvenanceCache(t *testing.T) {
	t.Helper()
	provenanceOnce = sync.Once{}
	provenanceCached = ""
	t.Cleanup(func() {
		provenanceOnce = sync.Once{}
		provenanceCached = ""
	})
}

// TestEnsureImage_UnverifiableProvenanceIsNotCurrent pins that the
// staleness gate must not FAIL OPEN. When the wanted provenance digest cannot
// be computed at all — an unresolvable host binary, an unreadable base
// Containerfile — imageStale answers "not stale", and accepting that as
// "current" let ANY present image, however old, satisfy a container request
// with no warning and no finding. It also made the unbuildable-stale finding
// unreachable in production: an unresolvable binary is exactly what empties the digest, so
// the gate returned before that branch could ever run.
func TestEnsureImage_UnverifiableProvenanceIsNotCurrent(t *testing.T) {
	resetStrictness(t)
	clearProvenanceCache(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-docker")
	writeInspectOKBuildFailScript(t, script, `{"ctxloom.provenance":"whatever-was-baked"}`)

	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { resolveSelfExe = orig })

	c := Container{
		runtime:    fakeRuntime{name: "docker", binary: script, available: true},
		image:      "ctxloom-agent-unverifiable-provenance-test:latest",
		engineSpec: engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")},
	}
	require.NoError(t, c.ensureImage(context.Background()),
		"an unverifiable image still launches — this must never take the container axis down")

	findings := strictness.All()
	require.Len(t, findings, 1,
		"a present image whose provenance cannot be computed must not pass silently as current")
	assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
	assert.Contains(t, findings[0].FixIt, "--degraded")
}

// TestEnsureImage_StaleRebuildFail_FatalUnlessDegraded pins that a PRESENT
// but STALE image whose refresh build fails still LAUNCHES the stale image
// (ensureImage returns nil — the container axis is never taken down), but in
// strict mode records a fatal ClassIsolation finding the choke owner aborts on
// (a stale pre-entrypoint image can run as root), while --degraded runs the
// stale image with no finding, exactly as before.
func TestEnsureImage_StaleRebuildFail_FatalUnlessDegraded(t *testing.T) {
	setup := func(t *testing.T) Container {
		withFakeSelfExe(t)
		t.Setenv("PATH", t.TempDir()) // no companions on PATH: staging warns + skips
		forceProvenance(t)
		dir := t.TempDir()
		script := filepath.Join(dir, "fake-docker")
		// A baked provenance that will never match the host digest → STALE.
		writeInspectOKBuildFailScript(t, script, `{"ctxloom.provenance":"stale-nomatch"}`)
		return Container{
			runtime:    fakeRuntime{name: "docker", binary: script, available: true},
			image:      "ctxloom-agent-stale-test:latest",
			engineSpec: engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")},
		}
	}

	t.Run("strict: one fatal finding; the stale image still launches", func(t *testing.T) {
		resetStrictness(t)
		c := setup(t)
		require.NoError(t, c.ensureImage(context.Background()),
			"the stale image still launches — a failed refresh never blocks the axis")

		findings := strictness.All()
		require.Len(t, findings, 1, "a stale image whose rebuild failed is exactly one fatal finding")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "STALE", "the finding must flag the stale image")
		assert.Contains(t, findings[0].FixIt, "--degraded", "the fix-it must name the escape hatch")
	})

	t.Run("degraded: runs the stale image with no finding", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		c := setup(t)
		require.NoError(t, c.ensureImage(context.Background()))
		assert.Empty(t, strictness.All(), "--degraded records nothing and runs the stale image as before")
	})
}

// TestEnsureImage_UserBaseBuildFail_FatalUnlessDegraded pins that a failed
// build from an EXPLICITLY-configured base Containerfile
// (isolation_base_containerfile) records a fatal ClassIsolation finding instead
// of silently falling through to a DIFFERENT base. The fallback sources still
// only warn; --degraded records nothing.
func TestEnsureImage_UserBaseBuildFail_FatalUnlessDegraded(t *testing.T) {
	setup := func(t *testing.T) Container {
		withFakeSelfExe(t)
		t.Setenv("PATH", t.TempDir())
		dir := t.TempDir()
		base := filepath.Join(dir, "Containerfile.base")
		require.NoError(t, os.WriteFile(base, []byte("FROM scratch\n"), 0o644))
		script := filepath.Join(dir, "fake-docker")
		writeAbsentBuildFailScript(t, script) // absent image → enter the build loop; builds fail
		return Container{
			runtime:           fakeRuntime{name: "docker", binary: script, available: true},
			image:             "ctxloom-agent-userbase-test:latest",
			baseContainerfile: base,
			engineSpec:        engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")},
		}
	}

	t.Run("strict: the configured base failure is one fatal finding; fallbacks warn", func(t *testing.T) {
		resetStrictness(t)
		c := setup(t)
		err := c.ensureImage(context.Background())
		require.Error(t, err, "all sources failed on an absent image, so ensure still errors")

		findings := strictness.All()
		require.Len(t, findings, 1, "only the configured user-base source is a finding; the fallbacks warn")
		assert.Equal(t, strictness.ClassIsolation, findings[0].Class)
		assert.Contains(t, findings[0].Message, "configured base Containerfile",
			"the finding must name the user-configured base that failed")
		assert.Contains(t, findings[0].FixIt, "isolation_base_containerfile")
	})

	t.Run("degraded: no finding — falls through to the next source as before", func(t *testing.T) {
		resetStrictness(t)
		strictness.SetDegraded(true)
		c := setup(t)
		_ = c.ensureImage(context.Background())
		assert.Empty(t, strictness.All())
	})
}

// TestSelectBuildRuntime_ExplicitPreferMustBeHonored pins that an explicitly
// requested build runtime (--runtime) that isn't the one selected fails loud
// rather than silently building into a DIFFERENT daemon; auto-detect (empty
// prefer) accepts whatever is reachable and only errors when none is.
func TestSelectBuildRuntime_ExplicitPreferMustBeHonored(t *testing.T) {
	t.Run("explicit prefer not selected → error (no silent wrong-daemon build)", func(t *testing.T) {
		stubRuntimeProbe(t, Docker{})
		_, err := selectBuildRuntime("podman")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "podman", "names what was requested")
		assert.Contains(t, err.Error(), "docker", "names what it refused to substitute")
	})

	t.Run("explicit prefer honored → ok", func(t *testing.T) {
		stubRuntimeProbe(t, Docker{})
		rt, err := selectBuildRuntime("docker")
		require.NoError(t, err)
		assert.Equal(t, "docker", rt.Name())
	})

	t.Run("auto (empty prefer) accepts whatever is reachable", func(t *testing.T) {
		stubRuntimeProbe(t, Docker{})
		rt, err := selectBuildRuntime("")
		require.NoError(t, err)
		assert.Equal(t, "docker", rt.Name())
	})

	t.Run("no runtime reachable → error", func(t *testing.T) {
		stubRuntimeProbe(t, Host{})
		_, err := selectBuildRuntime("podman")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no container runtime")
	})

	t.Run("an unknown prefer falls through to auto and is rejected as a mismatch", func(t *testing.T) {
		stubRuntimeProbe(t, Docker{})
		_, err := selectBuildRuntime("containerd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "containerd")
	})
}

// selfLinuxExe is the third answer in this repo to "where is the running
// binary", and it answers a different question from the other two: not "what
// do I re-invoke" but "is this file a linux ELF I can copy INTO an image".
// Hence the two gates that make it unusable as a general self-path helper — it
// errors on a non-linux host and on a non-ELF file, so the caller degrades to
// another build source instead of baking a binary the image cannot run. There
// is no bare-name fallback here: a PATH name cannot be copied into an image.
func TestSelfLinuxExe_ResolvedELFOrAnError(t *testing.T) {
	exe, err := selfLinuxExe()
	if runtime.GOOS != "linux" {
		require.Error(t, err, "a non-linux host must fail up front, not hand back an unusable path")
		assert.Empty(t, exe)
		return
	}
	require.NoError(t, err)
	require.True(t, filepath.IsAbs(exe), "an image ingredient is always a real absolute path, never a PATH name")
	assert.FileExists(t, exe)

	resolved, rerr := filepath.EvalSymlinks(exe)
	require.NoError(t, rerr)
	assert.Equal(t, resolved, exe, "the path is symlink-resolved: the FILE is what gets copied, not the name")

	f, oerr := elf.Open(exe)
	require.NoError(t, oerr, "the gate only passes an ELF")
	require.NoError(t, f.Close())
}

// TestBuildAgentImage_Characterization covers BuildAgentImage's guard arms
// before a complexity-reduction split: the mutually-exclusive base flags, an unresolvable
// devcontainer, a backend with no local build recipe, and the no-runtime
// refusal. Complexity reduction cannot make any test go red — behaviour is
// unchanged by definition — so these must be green on both sides of it.
func TestBuildAgentImage_Characterization(t *testing.T) {
	ctx := context.Background()

	t.Run("base image and base containerfile are mutually exclusive", func(t *testing.T) {
		_, err := BuildAgentImage(ctx, "claude-code", ImageBuildOptions{
			BaseImage: "some/base:1", BaseContainerfile: "/tmp/Containerfile",
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "mutually exclusive")
	})

	t.Run("an unresolvable project devcontainer is a hard error", func(t *testing.T) {
		root := t.TempDir()
		writeDevcontainer(t, root, `{ this is not json }`)
		_, err := BuildAgentImage(ctx, "claude-code", ImageBuildOptions{AppRoot: root})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "project devcontainer")
	})

	t.Run("a backend with no local build recipe is refused", func(t *testing.T) {
		_, err := BuildAgentImage(ctx, "no-such-engine", ImageBuildOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no local build recipe")
	})

	t.Run("no reachable runtime is refused before any build", func(t *testing.T) {
		orig := selectRuntimeProbe
		selectRuntimeProbe = func(string) Runtime { return Host{} }
		t.Cleanup(func() { selectRuntimeProbe = orig })

		_, err := BuildAgentImage(ctx, "claude-code", ImageBuildOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no container runtime")
	})

	t.Run("every source failing returns the last error", func(t *testing.T) {
		withFakeSelfExe(t)
		dir := t.TempDir()
		script := filepath.Join(dir, "fake-docker")
		writeAbsentBuildFailScript(t, script)
		orig := selectRuntimeProbe
		selectRuntimeProbe = func(string) Runtime {
			return fakeRuntime{name: "docker", binary: script, available: true}
		}
		t.Cleanup(func() { selectRuntimeProbe = orig })

		_, err := BuildAgentImage(ctx, "claude-code", ImageBuildOptions{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "build")
	})
}

// TestEnsureImage_FlightKeyDiscriminatesByRuntime completes the pin on
// the ensureImage single-flight key.
//
// TestEnsureImage_ParallelCallersShareOneBuild already pins the tag half of
// that key (dedup for one tag; a different tag is its own flight). This pins
// the RUNTIME half: the same tag under two different runtime binaries is two
// images in two different daemons, so it must be two flights, never one
// caller silently inheriting the other daemon's outcome.
func TestEnsureImage_FlightKeyDiscriminatesByRuntime(t *testing.T) {
	withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir())

	dir := t.TempDir()
	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}
	_, provenance, ok := composedIdentity(spec, "", nil, nil)
	require.True(t, ok)
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, provenance)

	newRuntime := func(name string) (Runtime, string) {
		log := filepath.Join(dir, name+".log")
		script := filepath.Join(dir, "fake-"+name)
		writeFakeRuntimeScript(t, script, log, t.TempDir(), labels)
		return fakeRuntime{name: name, binary: script, available: true}, log
	}

	dockerRT, dockerLog := newRuntime("docker")
	podmanRT, podmanLog := newRuntime("podman")

	image := "ctxloom-agent-flightkey-test:latest"
	require.NoError(t, (Container{runtime: dockerRT, image: image, engineSpec: spec}).ensureImage(context.Background()))
	require.NoError(t, (Container{runtime: podmanRT, image: image, engineSpec: spec}).ensureImage(context.Background()))

	assert.Len(t, buildInvocations(t, dockerLog), 2, "the first daemon builds base + agent stage")
	assert.Len(t, buildInvocations(t, podmanLog), 2,
		"the same tag in a DIFFERENT daemon is a different image and must build there too")
}

// TestBaseContentKeysBothTags PARTIALLY refutes a finding, which observed that
// buildBaseImage reads the base Containerfile a SECOND time after
// composedIdentity's read and concluded the tag is derived from a read the
// rest of the build may not share. The two reads are real. The consequence is
// not: BOTH tags are content-keyed off the same bytes, so a file that changes
// between the reads cannot produce a wrongly-labelled image anyone launches —
// the next resolution computes a DIFFERENT agent tag, which is simply absent
// and therefore built, and a provenance mismatch forces exactly one rebuild.
// The divergence is self-correcting by construction rather than latent.
//
// The genuine residue was that buildBaseImage re-implemented the read instead
// of using baseStage.content(); that is now routed through the accessor. This
// pins the property that makes the consequence false.
func TestBaseContentKeysBothTags(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Containerfile.base")
	require.NoError(t, os.WriteFile(path, []byte("FROM debian:13\n"), 0o644))

	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}
	stage := userBaseStage(path)

	firstContent, err := stage.content()
	require.NoError(t, err)
	firstAgentTag, firstProvenance, ok := composedIdentity(spec, path, nil, nil)
	require.True(t, ok)
	firstBaseTag := baseImageTagFor(firstContent)

	require.NoError(t, os.WriteFile(path, []byte("FROM debian:12\n"), 0o644))

	secondContent, err := stage.content()
	require.NoError(t, err)
	secondAgentTag, secondProvenance, ok := composedIdentity(spec, path, nil, nil)
	require.True(t, ok)

	assert.NotEqual(t, firstBaseTag, baseImageTagFor(secondContent),
		"the base tag tracks the base file's content")
	assert.NotEqual(t, firstAgentTag, secondAgentTag,
		"so does the composed agent tag — an edit lands on a fresh, absent tag rather than reusing a stale one")
	if firstProvenance != "" {
		assert.NotEqual(t, firstProvenance, secondProvenance,
			"and the provenance label, so a mismatched image is flagged stale exactly once")
	}
}
