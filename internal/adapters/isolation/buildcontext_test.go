package isolation

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// buildCtxFixture is a devcontainer-style build: a Dockerfile inside a
// context directory holding a couple of files the build may COPY.
func buildCtxFixture(t *testing.T) (dockerfile, ctxDir string) {
	t.Helper()
	ctxDir = t.TempDir()
	dockerfile = filepath.Join(ctxDir, "Dockerfile")
	require.NoError(t, os.WriteFile(dockerfile, []byte("FROM debian:13\nCOPY . /src\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ctxDir, "scripts"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scripts", "setup.sh"), []byte("echo one\n"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 1\n"), 0o644))
	return dockerfile, ctxDir
}

// stageTags is every tag a base stage keys: the stage-1 base tag and the
// composed agent tag (with its provenance), both derived through identity().
func stageTags(t *testing.T, stage *baseStage) (baseTag, agentTag string) {
	t.Helper()
	key, err := stage.identity()
	require.NoError(t, err)
	spec := engineContainerSpec{engineInstall: []byte("RUN echo fake-install\n")}
	id, ok := composedIdentity(spec, stage, "claude-code")
	require.True(t, ok)
	return baseImageTagFor(key), id.ref
}

func TestBaseIdentity_IdenticalInputsGiveIdenticalTags(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	args := map[string]string{"B": "2", "A": "1"}
	b1, a1 := stageTags(t, devcontainerBuildStage("one", df, ctxDir, args))
	b2, a2 := stageTags(t, devcontainerBuildStage("two", df, ctxDir, map[string]string{"A": "1", "B": "2"}))
	assert.Equal(t, b1, b2, "the same Dockerfile, args and context must share one base tag")
	assert.Equal(t, a1, a2, "and one composed agent tag")
}

func TestBaseIdentity_BuildArgChangeChangesTag(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	b1, a1 := stageTags(t, devcontainerBuildStage("d", df, ctxDir, map[string]string{"VARIANT": "bookworm"}))
	b2, a2 := stageTags(t, devcontainerBuildStage("d", df, ctxDir, map[string]string{"VARIANT": "trixie"}))
	assert.NotEqual(t, b1, b2, "a changed build.args value builds a different image, so it must land on a fresh base tag")
	assert.NotEqual(t, a1, a2, "and a fresh composed agent tag")

	b3, _ := stageTags(t, devcontainerBuildStage("d", df, ctxDir, nil))
	assert.NotEqual(t, b1, b3, "dropping an arg changes the tag too")
}

func TestBaseIdentity_ContextFileChangeChangesTag(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, a1 := stageTags(t, stage)

	// Same size, so a size-only key would miss it too.
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scripts", "setup.sh"), []byte("echo two\n"), 0o755))
	b2, a2 := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "an edited context file is a changed build input")
	assert.NotEqual(t, a1, a2)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scripts", "new.sh"), []byte(""), 0o644))
	b3, _ := stageTags(t, stage)
	assert.NotEqual(t, b2, b3, "an added (even empty) context file changes the tag")

	require.NoError(t, os.Chmod(filepath.Join(ctxDir, "scripts", "new.sh"), 0o755))
	b4, _ := stageTags(t, stage)
	assert.NotEqual(t, b3, b4, "a mode change is copied into the image by COPY, so it changes the tag")
}

func TestBaseIdentity_DockerignoredFileDoesNotChangeTag(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("*.log\nscratch/\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ctxDir, "scratch"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "a"), []byte("1"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 2, longer\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "a"), []byte("2"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "b"), []byte("new"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.Equal(t, b1, b2, "files the build never sends (.dockerignore) cannot change the image, so they must not churn the tag")

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("*.log\n"), 0o644))
	b3, _ := stageTags(t, stage)
	assert.NotEqual(t, b2, b3, "an ignore edit that changes what is sent changes the tag")
}

// An exception pattern re-includes a file under an excluded directory: the
// walk must not prune that directory wholesale.
func TestBaseIdentity_DockerignoreExceptionIsHashed(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("scratch\n!scratch/keep\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ctxDir, "scratch"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "keep"), []byte("1"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "keep"), []byte("2"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "a !-re-included file is sent, so it is a build input")
}

// docker reads only .dockerignore; podman prefers .containerignore. A file
// only .containerignore excludes is still sent by docker, so it must still
// key the tag — erring toward a rebuild, never toward a stale image.
func TestBaseIdentity_ContainerignoreAloneDoesNotHideAFile(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".containerignore"), []byte("*.log\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 2\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "docker would send build.log, so a change to it must change the tag")
}

// The inverse: .dockerignore excludes a file but .containerignore (which
// podman reads INSTEAD of .dockerignore) does not, so podman sends it.
func TestBaseIdentity_DockerignoreAloneDoesNotHideAFileFromPodman(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("*.log\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".containerignore"), []byte("scratch\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 2\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "podman would send build.log, so a change to it must change the tag")
}

// BuildKit's per-Dockerfile ignore file (<Dockerfile>.dockerignore) REPLACES
// the context's .dockerignore for docker, so when it does not exclude a file
// docker sends that file, whatever .dockerignore says.
func TestBaseIdentity_PerDockerfileIgnoreTakesPrecedenceForDocker(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("*.log\n"), 0o644))
	require.NoError(t, os.WriteFile(df+".dockerignore", []byte("scratch\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 2\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "docker reads Dockerfile.dockerignore instead of .dockerignore, so it sends build.log")

	require.NoError(t, os.WriteFile(df+".dockerignore", []byte("*.log\n"), 0o644))
	b3, _ := stageTags(t, stage)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 3\n"), 0o644))
	b4, _ := stageTags(t, stage)
	assert.Equal(t, b3, b4, "excluded by both runtimes' ignore files, build.log is never sent")
}

// The embedded default and synthetic FROM bases have no context and no args:
// their key is exactly the Containerfile bytes, so their tags are unchanged.
func TestBaseIdentity_EmbeddedStageKeyIsItsContainerfile(t *testing.T) {
	for _, st := range []*baseStage{defaultBaseStage(), imageRefBaseStage("debian:13")} {
		key, err := st.identity()
		require.NoError(t, err)
		assert.Equal(t, st.containerfile, key)
	}
}

// Past the content budget the rest of the context is keyed by metadata, and
// that degrade is announced — the key stays deterministic and still sees a
// size change.
func TestBaseIdentity_OverBudgetDegradesLoudly(t *testing.T) {
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))
	old := contextHashBudget
	contextHashBudget = 4
	t.Cleanup(func() { contextHashBudget = old })

	df, ctxDir := buildCtxFixture(t)
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)
	b1again, _ := stageTags(t, stage)
	assert.Equal(t, b1, b1again, "the degraded key is still deterministic")
	assert.Contains(t, sink.String(), "build context", "the degrade is announced")
	assert.Contains(t, sink.String(), ".dockerignore", "and names the remedy")

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("a longer line\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "a size change is still seen past the budget")
}

// TestBaseIdentity_BudgetIsSpentAcrossFiles pins that the content budget is a
// running total over the whole context, not a per-file size cap: every file
// here fits the budget on its own, but together they exceed it, so the later
// ones are keyed by size and modification time and the degrade is announced.
func TestBaseIdentity_BudgetIsSpentAcrossFiles(t *testing.T) {
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	df, ctxDir := buildCtxFixture(t)
	dfInfo, err := os.Stat(df)
	require.NoError(t, err)
	old := contextHashBudget
	contextHashBudget = dfInfo.Size() + 1 // the largest file fits; the sum does not
	t.Cleanup(func() { contextHashBudget = old })

	stageTags(t, devcontainerBuildStage("d", df, ctxDir, nil))
	assert.Contains(t, sink.String(), "build context", "files past the spent budget degrade, loudly")
}

// TestBaseIdentity_SymlinkTargetChangesTag pins that a symlink in the context
// is keyed by its target: COPY copies the link itself, so repointing it is a
// changed build input even though no file content changed.
func TestBaseIdentity_SymlinkTargetChangesTag(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	link := filepath.Join(ctxDir, "current")
	require.NoError(t, os.Symlink("scripts/setup.sh", link))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink("build.log", link))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "a repointed symlink is a changed build input")
}

// TestWriteKeyField_FieldBoundariesAreUnambiguous pins the length prefix: two
// fields must never hash like one field whose value spells out both, or a
// build arg could be crafted to collide with a different set of args.
func TestWriteKeyField_FieldBoundariesAreUnambiguous(t *testing.T) {
	two := sha256.New()
	writeKeyField(two, "arg", []byte("A=1"))
	writeKeyField(two, "arg", []byte("B=2"))

	one := sha256.New()
	writeKeyField(one, "arg", []byte("A=1arg\x00B=2"))

	assert.NotEqual(t, two.Sum(nil), one.Sum(nil))
}
