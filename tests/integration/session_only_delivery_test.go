//go:build integration

package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// A session delivers only into its session home (ruled 2026-09-21):
// `ctxloom run` with a binding that selects no root writes neither the
// project tree nor the user's real home. The project's own ctxloom state
// (.ctxloom/state, the project-id a first run mints) and ctxloom's own home
// (~/.ctxloom — the session's directory, the ownership record, logs) are
// ctxloom's, not an engine file, and are the ONLY things a run may touch
// outside the session. Asserted on the filesystem: a byte-level snapshot
// before and after.

// treeSnapshot is every regular file under root, keyed by its slash path
// relative to root, with the sha256 of its bytes. Paths under an excluded
// prefix are not recorded.
func treeSnapshot(t *testing.T, root string, exclude ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, p)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)
		for _, ex := range exclude {
			if rel == ex || strings.HasPrefix(rel, ex+"/") {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		sum := sha256.Sum256(b)
		out[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	require.NoError(t, err)
	return out
}

// sessionDirs lists the harp directories under the fake home's sessions
// root, so a test can find the one run it just made.
func sessionDirs(t *testing.T, env *testenv.TestEnvironment) []string {
	t.Helper()
	root := filepath.Join(env.HomeDir, paths.AppDirName, paths.SessionsDir)
	entries, err := os.ReadDir(root)
	require.NoError(t, err, "the run left no session directory under %s", root)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs
}

// findUnder reports every file under dir whose base name is name.
func findUnder(t *testing.T, dir, name string) []string {
	t.Helper()
	var hits []string
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == name {
			hits = append(hits, p)
		}
		return nil
	})
	return hits
}

// mockProjectFiles are the mock engine's well-known project-side files: the
// ones an explicit project-root selection lands and a default run never
// does.
var mockProjectFiles = []string{"MOCK_CONTEXT.md", ".mock/mcp.json", ".mock/settings.json", ".mock/commands", ".mock/skills"}

// projectExcluded are the project paths a run legitimately touches: its
// own state, the project identity a first run mints, and git's.
var projectExcluded = []string{".ctxloom/state", ".ctxloom/project-id", ".git"}

func setupSessionOnlyProject(t *testing.T) (*testenv.TestEnvironment, *testenv.MockLM) {
	t.Helper()
	env := setupTestEnv(t)
	mockLM, err := env.SetupMockLM()
	require.NoError(t, err)
	require.NoError(t, mockLM.SetResponse("MOCK-REPLY"))
	writeFragment(t, env, "rules", []string{"rules"}, "Project rules for the session.")
	writeProfile(t, env, "dev", "name: dev\ndescription: dev\nbundles:\n  - local#fragments/rules\n")
	return env, mockLM
}

// TestRun_DefaultBindingWritesOnlyTheSession: after a run on the mock with
// a default binding, the project tree and the fake real home are
// byte-identical before and after — except the project's .ctxloom/state and
// ctxloom's own ~/.ctxloom — while the session's directory carries the
// mock's context.
func TestRun_DefaultBindingWritesOnlyTheSession(t *testing.T) {
	env, _ := setupSessionOnlyProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	projectBefore := treeSnapshot(t, env.ProjectDir, projectExcluded...)
	homeBefore := treeSnapshot(t, env.HomeDir, paths.AppDirName)

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	require.Contains(t, env.LastOutput(), "MOCK-REPLY")

	assert.Equal(t, projectBefore, treeSnapshot(t, env.ProjectDir, projectExcluded...),
		"a default run wrote the project tree")
	assert.Equal(t, homeBefore, treeSnapshot(t, env.HomeDir, paths.AppDirName),
		"a default run wrote the user's real home outside ~/.ctxloom")
	for _, f := range mockProjectFiles {
		assert.NoFileExists(t, filepath.Join(env.ProjectDir, f), "the mock's project-side file %s landed under a default binding", f)
	}

	dirs := sessionDirs(t, env)
	require.NotEmpty(t, dirs)
	var carried []string
	for _, d := range dirs {
		carried = append(carried, findUnder(t, d, "MOCK_CONTEXT.md")...)
	}
	assert.NotEmpty(t, carried, "the session carries the mock's context under %v", dirs)
}

// TestRun_ProjectRootSelectedForOneSurface: a binding whose `roots:` selects
// the project root for the context surface lands that surface — and only
// that surface — in the project; the dry-run plan names the selection
// unsafe.
func TestRun_ProjectRootSelectedForOneSurface(t *testing.T) {
	env, _ := setupSessionOnlyProject(t)
	_ = env.Run("agent", "create", "dev", "--profiles", "dev", "--llm", "mock", "--root", "context=project-root")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())

	_ = env.Run("run", "--agent", "dev", "--dry-run", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	assert.Contains(t, env.LastOutput(), "unsafe", "the dry-run plan names the project-root selection unsafe")

	_ = env.Run("run", "--agent", "dev", "--one-shot", "unicorn-prompt")
	require.Equal(t, 0, env.LastExitCode(), env.LastOutput())
	require.Contains(t, env.LastOutput(), "MOCK-REPLY")

	assert.FileExists(t, filepath.Join(env.ProjectDir, "MOCK_CONTEXT.md"), "the selected surface lands in the project")
	for _, f := range mockProjectFiles[1:] {
		assert.NoFileExists(t, filepath.Join(env.ProjectDir, f), "an unselected surface %s landed in the project", f)
	}
	assert.NoDirExists(t, filepath.Join(env.HomeDir, ".mock"), "the real home is never written")
}
