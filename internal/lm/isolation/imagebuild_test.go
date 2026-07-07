package isolation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containerfiles "github.com/ctxloom/ctxloom/container"
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

// TestTailLines bounds the failure diagnostics to the last n lines.
func TestTailLines(t *testing.T) {
	assert.Equal(t, "b\nc", tailLines("a\nb\nc\n", 2))
	assert.Equal(t, "a\nb\nc", tailLines("a\nb\nc", 5), "short input passes through whole")
}

// TestEnsureImage_ParallelCallersShareOneBuild: a fan-out drives N members
// through ensureImage for the SAME absent tag concurrently — exactly one build
// must run, every caller sharing its outcome (pre-dedup, each member raced its
// own build and a mid-build untag could flake another's post-build recheck).
// A later, non-overlapping ensure re-checks presence instead of rebuilding,
// and a DIFFERENT tag still builds independently — flights are per-tag.
func TestEnsureImage_ParallelCallersShareOneBuild(t *testing.T) {
	withFakeSelfExe(t)
	t.Setenv("PATH", t.TempDir()) // no companions on PATH: staging warns + skips

	dir := t.TempDir()
	logFile := filepath.Join(dir, "builds.log")
	// The fake runtime reports the just-built image's provenance label as
	// current, so a caller landing after the flight re-checks cheaply instead
	// of rebuilding.
	labels := fmt.Sprintf(`{"ctxloom.provenance":%q}`, HostProvenanceDigest(""))
	script := filepath.Join(dir, "fake-docker")
	writeFakeRuntimeScript(t, script, logFile, dir, labels)

	c := Container{
		runtime: fakeRuntime{name: "docker", binary: script, available: true},
		image:   "ctxloom-agent-dedup-test:latest",
		profile: containerProfile{officialImage: "example/client:1"},
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
	assert.Len(t, buildInvocations(t, logFile), 1, "one build per tag, not one per fan-out member")

	require.NoError(t, c.ensureImage(context.Background()))
	assert.Len(t, buildInvocations(t, logFile), 1, "a present, current image never rebuilds")

	c2 := c
	c2.image = "ctxloom-agent-dedup-test-2:latest"
	require.NoError(t, c2.ensureImage(context.Background()))
	assert.Len(t, buildInvocations(t, logFile), 2, "a different tag is its own flight and builds")
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
	assert.Equal(t, "bin-digest-"+baseContentHash(containerfiles.Base), deflt)

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
