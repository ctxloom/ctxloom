package launch_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
)

// TestResolve_DirtyTree_SettledOnce_RequestThenConfigThenBuiltIn: the
// dirty-tree handler a worktree cell acts on is decided HERE, once, and the
// cell receives it settled — the caller's per-invocation value, else the
// project's `dirty_tree_handler:` default, else the built-in "commit". A
// Source that says nothing resolves to the project's handler, never straight
// to "commit": the built-in is the member that auto-commits the user's
// branch, and a project that pinned "fail" must land there.
func TestResolve_DirtyTree_SettledOnce_RequestThenConfigThenBuiltIn(t *testing.T) {
	cases := []struct {
		name    string
		project string
		src     launch.DirtyTreeHandler
		want    launch.DirtyTreeHandler
	}{
		{"a silent Source takes the project's handler", "fail", "", launch.DirtyTreeHandlerFail},
		{"the request wins over the project's handler", "fail", launch.DirtyTreeHandlerStale, launch.DirtyTreeHandlerStale},
		{"silence at both levels is the built-in commit", "", "", launch.DirtyTreeHandlerCommit},
		{"the request alone is itself", "", launch.DirtyTreeHandlerCopy, launch.DirtyTreeHandlerCopy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := launchtest.Deps(t, launchtest.ProjectDirtyTree(tc.project))
			l, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
				Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project,
				Workspace: launch.WorkspaceWorktree, DirtyTree: tc.src,
			})
			require.NoError(t, err)
			t.Cleanup(func() { _ = launch.Discard(context.Background(), l) })
			require.Equal(t, tc.want, env.LastCellRequest().DirtyTree, "the cell acts on the handler the resolver settled")
		})
	}
}

// TestResolve_DirtyTree_UnusableProjectDefaultIsRefused: a project default
// that does not parse refuses the launch by name, with the remedy. It must
// not fall through to the built-in: reaching "commit" through a spelling
// nobody recognised routes around the consent the commit handler is gated
// on.
func TestResolve_DirtyTree_UnusableProjectDefaultIsRefused(t *testing.T) {
	env := launchtest.Deps(t, launchtest.ProjectDirtyTree("comit"))
	_, err := launch.Resolve(context.Background(), env.Deps, launch.Source{
		Identity: env.Identity, Agent: "setup", Mode: engine.Structured, Permission: "bypass", Prompt: "x", WorkDir: env.Project,
	})
	require.Error(t, err)
	require.ErrorContains(t, err, "dirty_tree_handler")
	require.ErrorContains(t, err, "comit")
	require.ErrorContains(t, err, ".ctxloom/config.yaml")
}
