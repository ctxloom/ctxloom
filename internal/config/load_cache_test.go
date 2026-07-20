package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeProjectConfig lays a project .ctxloom/config.yaml under a fresh tempdir,
// chdirs into it (Load discovers by walking up from cwd), and returns the
// config path. HOME is junked so the home fallback can never be what we measure.
func writeProjectConfig(t *testing.T, body string) string {
	t.Helper()
	// ProjectDir isolates the environment (HOME included, so the home fallback
	// can never be what we measure) and chdirs into a fresh temp dir — Load
	// discovers the project by walking up from cwd.
	root := testsupport.ProjectDir(t)
	appDir := filepath.Join(root, AppDirName)
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	path := paths.ConfigPath(appDir)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))

	Invalidate()
	t.Cleanup(Invalidate)
	return path
}

// TestLoad_MemoizesAmbientConfig is the point of the cache: the no-arg Load is
// the ambient project config and must be parsed once, not re-read from disk by
// each of the ~35 call sites.
func TestLoad_MemoizesAmbientConfig(t *testing.T) {
	writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	first, err := Load()
	require.NoError(t, err)
	second, err := Load()
	require.NoError(t, err)

	assert.Same(t, first, second, "the no-arg Load must return the memoized ambient config")
}

// TestLoad_SeesConfigRewrittenOnDisk is the constraint a naive singleton breaks
// in TWO places: init writes config.yaml between two Loads (read-after-write),
// and agent_run re-loads on every spawn so edited agent definitions take effect
// mid-session (hot reload). The memo must be validated against the file, never
// frozen at first read.
func TestLoad_SeesConfigRewrittenOnDisk(t *testing.T) {
	path := writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	first, err := Load()
	require.NoError(t, err)
	require.Equal(t, "alpha", first.defaultAgent)

	// Rewrite on disk, exactly as init's scaffold / a user's editor / Save would.
	require.NoError(t, os.WriteFile(path, []byte("version: 6\ndefault_agent: beta\n"), 0o644))

	updated, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "beta", updated.defaultAgent,
		"a rewritten config.yaml must be picked up: init read-after-write and agent_run hot reload both depend on it")
}

// TestLoadFresh_IsUncachedAndIndependent pins the mutator path: a caller that
// mutates a Config before Save must not be handed the shared ambient instance,
// or a mutation abandoned on an error path would poison every later reader.
func TestLoadFresh_IsUncachedAndIndependent(t *testing.T) {
	writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	ambient, err := Load()
	require.NoError(t, err)

	mutable, err := LoadFresh()
	require.NoError(t, err)
	assert.NotSame(t, ambient, mutable, "LoadFresh must not hand back the shared ambient config")

	// A mutation abandoned without Save must not be observable by readers.
	mutable.defaultAgent = "poisoned"
	again, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "alpha", again.defaultAgent, "an unsaved mutation must never leak into the ambient config")
}

// TestLoad_WithAppDir_IsUncachedAndDoesNotPoisonAmbient pins the third class of
// load: an explicit target (acp --app-dir, a worktree's .ctxloom) is a DIFFERENT
// config and must neither be served from nor written into the ambient memo.
func TestLoad_WithAppDir_IsUncachedAndDoesNotPoisonAmbient(t *testing.T) {
	writeProjectConfig(t, "version: 6\ndefault_agent: alpha\n")

	other := t.TempDir()
	otherApp := filepath.Join(other, AppDirName)
	require.NoError(t, os.MkdirAll(otherApp, 0o755))
	require.NoError(t, os.WriteFile(paths.ConfigPath(otherApp),
		[]byte("version: 6\ndefault_agent: elsewhere\n"), 0o644))

	targeted, err := Load(WithAppDir(otherApp))
	require.NoError(t, err)
	assert.Equal(t, "elsewhere", targeted.defaultAgent)

	ambient, err := Load()
	require.NoError(t, err)
	assert.Equal(t, "alpha", ambient.defaultAgent,
		"an explicit --app-dir load must not become the ambient config")
}
