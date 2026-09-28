package isolation_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// envWorkspace is NOT the engine's config-home carrier, and this pins it: a
// prepared worktree's env names only what the worktree itself provisioned —
// its scratch dir and git identity — and never the engine's declared home var.
// The controlled home is resolved ONCE, off the agent binding, for every cell
// (operations.ResolveInTreeAgentHome); a second carrier here is exactly how a
// run's home came to depend on which workspace it happened to pick.
//
// External test package on purpose: the var under test is read from the
// engine's OWN declaration through the engine's kind, which isolation
// cannot import in production (backends imports isolation). An external test's
// imports are XTestImports and add no production edge.
func TestWorktreeWorkspace_EnvCarriesNoEngineHomeVar(t *testing.T) {
	strictness.Reset()
	t.Cleanup(func() { strictness.Reset() })
	t.Setenv("HOME", t.TempDir())
	// The shipped declaration, read from the composed registry only. NOT
	// enginefixture.MustComposeShipped: that also installs the registry as
	// this package's facts accessor, for good, replacing TestMain's fixture
	// facts under every test that runs after this one.
	kind, ok := engines.Registry().Lookup("claude-code")
	require.True(t, ok)
	require.True(t, kind.Home().Relocates(), "claude-code declares a relocatable home; without one there is nothing to assert against")
	homeVar := kind.Home().Vars[0].Name

	ws, err := isolation.NewWorktree(&git.Fake{CommonDirValue: t.TempDir()}).prepareWorkspace(context.Background(), "/proj", "member-a")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ws.Cleanup() })

	env := isolation.WorkspaceEnv(ws)
	assert.NotContains(t, env, homeVar,
		"a worktree's env must not carry the engine's home var — the home is decided off the binding, not the workspace")
	assert.NotContains(t, env, "HOME", "no blanket HOME override either")
	assert.Contains(t, env, "TMPDIR", "the scratch dir the worktree provisioned is what its env is for")
	assert.Contains(t, env, "GIT_AUTHOR_NAME", "so is the git identity for the checkout it created")
}
