package isolation

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// TestBaseIdentity_AnExcludedFileHidesNoSibling: an ignored file is skipped
// alone — the files the walk meets after it in the same directory are still
// sent, so still keyed.
func TestBaseIdentity_AnExcludedFileHidesNoSibling(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("*.log\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	// "scripts" sorts after the ignored "build.log".
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scripts", "setup.sh"), []byte("echo two\n"), 0o755))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "a file after an ignored one is still a build input")
}

// TestBaseIdentity_PodmansExceptionAloneIsHashed: the mirror of
// TestBaseIdentity_OneRuntimesExceptionIsHashed — the exception is podman's
// (.containerignore) and docker's .dockerignore excludes the whole directory.
func TestBaseIdentity_PodmansExceptionAloneIsHashed(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte("scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".containerignore"), []byte("scratch\n!scratch/keep\n"), 0o644))
	require.NoError(t, os.MkdirAll(filepath.Join(ctxDir, "scratch"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "keep"), []byte("1"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "scratch", "keep"), []byte("2"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "podman sends scratch/keep, so a change to it must change the tag")
}

// TestBaseIdentity_OnlyDockersIgnoreFileHidesNothing: a per-Dockerfile ignore
// with no context ignore file at all excludes for docker only — podman, with
// nothing to read, sends everything.
func TestBaseIdentity_OnlyDockersIgnoreFileHidesNothing(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(df+".dockerignore", []byte("*.log\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "build.log"), []byte("noise 2\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "podman would send build.log")
}

// TestBaseIdentity_TheConfiguredContextIsTheOneKeyed: a build.context apart
// from the Dockerfile's directory is what the runtime sends, so it is what the
// key reads.
func TestBaseIdentity_TheConfiguredContextIsTheOneKeyed(t *testing.T) {
	dfDir, ctxDir := t.TempDir(), t.TempDir()
	df := filepath.Join(dfDir, "Dockerfile")
	require.NoError(t, os.WriteFile(df, []byte("FROM debian:13\nCOPY . /src\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "app.txt"), []byte("one\n"), 0o644))
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "app.txt"), []byte("two\n"), 0o644))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2, "an edit inside the configured context is a build input")
}

// TestBaseIdentity_ARenameChangesTag: COPY lands a file at its path, so moving
// it — same content, same mode — is a changed build input.
func TestBaseIdentity_ARenameChangesTag(t *testing.T) {
	df, ctxDir := buildCtxFixture(t)
	stage := devcontainerBuildStage("d", df, ctxDir, nil)
	b1, _ := stageTags(t, stage)

	require.NoError(t, os.Rename(filepath.Join(ctxDir, "scripts", "setup.sh"), filepath.Join(ctxDir, "scripts", "set.sh")))
	b2, _ := stageTags(t, stage)
	assert.NotEqual(t, b1, b2)
}

// TestBaseIdentity_AnUnreadableInputIsAnError: an ignore file that cannot be
// read (here: a directory, or a line past the scanner's limit) or a context
// that does not exist is never keyed as if it were absent — the key would
// then describe a different build than the runtime runs.
func TestBaseIdentity_AnUnreadableInputIsAnError(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, df, ctxDir string) (context string)
	}{
		{"the per-Dockerfile ignore is a directory", func(t *testing.T, df, ctxDir string) string {
			require.NoError(t, os.Mkdir(df+".dockerignore", 0o755))
			return ctxDir
		}},
		{".dockerignore holds a line too long to read", func(t *testing.T, _, ctxDir string) string {
			require.NoError(t, os.WriteFile(filepath.Join(ctxDir, ".dockerignore"), []byte(strings.Repeat("a", 70_000)+"\n"), 0o644))
			return ctxDir
		}},
		{".containerignore is a directory", func(t *testing.T, _, ctxDir string) string {
			require.NoError(t, os.Mkdir(filepath.Join(ctxDir, ".containerignore"), 0o755))
			return ctxDir
		}},
		{"the context does not exist", func(_ *testing.T, _, ctxDir string) string {
			return filepath.Join(ctxDir, "missing")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			df, ctxDir := buildCtxFixture(t)
			stage := devcontainerBuildStage("d", df, c.setup(t, df, ctxDir), nil)
			_, err := stage.identity()
			require.ErrorContains(t, err, "build context")
		})
	}
}

// TestBaseIdentity_TheDegradeNamesTheBudget: the warning states the budget the
// context went past, in MiB.
func TestBaseIdentity_TheDegradeNamesTheBudget(t *testing.T) {
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)
	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))
	old := contextHashBudget
	contextHashBudget = 3 << 20
	t.Cleanup(func() { contextHashBudget = old })

	df, ctxDir := buildCtxFixture(t)
	require.NoError(t, os.WriteFile(filepath.Join(ctxDir, "big.bin"), make([]byte, 3<<20+1), 0o644))
	stageTags(t, devcontainerBuildStage("d", df, ctxDir, nil))
	assert.Contains(t, sink.String(), "holds more than 3 MiB")
}

// TestWriteKeyField_TheTagIsKeyed: the same value under two tags (a build arg
// and a context path, say) is two different inputs.
func TestWriteKeyField_TheTagIsKeyed(t *testing.T) {
	arg := sha256.New()
	writeKeyField(arg, "arg", []byte("x"))
	path := sha256.New()
	writeKeyField(path, "path", []byte("x"))
	assert.NotEqual(t, arg.Sum(nil), path.Sum(nil))
}
