package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// remoteRemoveProject builds an isolated project with one registered remote
// ("origin"), seeded directly through remote.Registry (no network probe —
// see remote_update_lockfile_scope_test.go's updateScopeFixture for the same
// pattern), and returns the config over it.
func remoteRemoveProject(t *testing.T) *config.Config {
	t.Helper()
	root := testsupport.ProjectDir(t)
	appDir := filepath.Join(root, ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	registry, err := remote.NewRegistry(filepath.Join(appDir, "remotes.yaml"))
	require.NoError(t, err)
	require.NoError(t, registry.Add("origin", "https://github.com/alice/ctxloom"))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

// remoteListedByName reports whether ListRemotes currently returns a remote
// by that name.
func remoteListedByName(t *testing.T, cfg *config.Config, name string) bool {
	t.Helper()
	list, err := operations.ListRemotes(context.Background(), cfg, operations.ListRemotesRequest{})
	require.NoError(t, err)
	for _, r := range list.Remotes {
		if r.Name == name {
			return true
		}
	}
	return false
}

// TestRunRemoteRemove_BareReportsAndDestroysNothing pins the report side of
// `remote remove`'s safety posture: a bare invocation (remoteRemoveYes ==
// false) must say plainly that nothing was removed, name the --yes command
// to apply it, and — the assertion that actually catches a broken guard —
// leave the remote registered. A "preview" that quietly removed it anyway
// would pass a test that only checks exit code or the report text; this one
// re-lists the registry.
func TestRunRemoteRemove_BareReportsAndDestroysNothing(t *testing.T) {
	cfg := remoteRemoveProject(t)

	remoteRemoveYes = false
	cmd, buf := testCmd()
	require.NoError(t, runRemoteRemove(cmd, []string{"origin"}))
	out := buf.String()
	assert.Contains(t, out, "Nothing was removed")
	assert.Contains(t, out, "--yes")

	assert.True(t, remoteListedByName(t, cfg, "origin"), "the bare (no --yes) path must leave the remote registered")
}

// TestRunRemoteRemove_YesDestroys pins the apply side: remoteRemoveYes ==
// true must actually remove the remote from the registry, not just print
// that it did. Paired with the bare-path test above so a regression in
// either direction — bare destroys, or --yes no-ops — is caught by an
// assertion on the registry's contents.
func TestRunRemoteRemove_YesDestroys(t *testing.T) {
	cfg := remoteRemoveProject(t)

	remoteRemoveYes = true
	t.Cleanup(func() { remoteRemoveYes = false })
	cmd, buf := testCmd()
	require.NoError(t, runRemoteRemove(cmd, []string{"origin"}))
	assert.Contains(t, buf.String(), "Removed remote")

	assert.False(t, remoteListedByName(t, cfg, "origin"), "--yes must actually remove the remote from the registry")
}
