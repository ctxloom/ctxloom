//go:build !darwin

// claude's login store is a directory under $HOME only off macOS
// (loginStoreHomeRel); on macOS it is the Keychain, sessionhome_darwin_test.go.

package isolation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/pkg/clifmt"
)

// CONTAINER + LOGIN: the human's credential storage is mounted read-write
// at its place under the container's $HOME — never the session home, never
// $HOME itself — and the storage var is blanked, which points claude there.
// The session home keeps its own mount beside it.
func TestCredentials_ContainerLoginMountsTheStoreUnderHome(t *testing.T) {
	home := fakeHostHome(t, tokenFixture)
	creds := claudeCredentials(t, engine.AuthLogin)
	s := credSpec(t, claudeEngine(t), home, harpA, agents.HomeModeSession, creds)

	pl, mounts := placeOn(t, s, t.TempDir(), containerOf)
	store := mount{Host: filepath.Join(home, ".claude"), Container: defaultContainerHome + "/.claude"}
	assert.Contains(t, mounts, store, "the human's storage at $HOME/.claude, read-write: claude refreshes it")
	assert.Equal(t, "", pl.Env[claude.SecureStorageEnv], "blank: claude reads $HOME/.claude")
	assert.Contains(t, pl.Env, claude.SecureStorageEnv, "set to empty, not left to inherit")
	assert.Equal(t, defaultContainerInstanceHome+"/"+claude.HomeLeaf, pl.Env[claude.ConfigDirEnv], "config stays in the session home")
	assert.Equal(t, creds.Unset, pl.Unset)
	for _, m := range mounts {
		assert.NotEqual(t, defaultContainerHome, m.Container, "nothing is mounted AS $HOME for a relocating engine")
	}
	assert.NoFileExists(t, filepath.Join(claudeHome(home, harpA), ".credentials.json"), "shared by mount, never copied")
	assert.Empty(t, strictness.All())
}

// F7: a declared store that is missing refuses the run on EVERY environment
// — the host would start logged out just as the container would — with a
// typed error and a remedy naming the directory and `auth: token`.
func TestCredentials_AMissingLoginStoreRefusesOnEveryEnvironment(t *testing.T) {
	home := fakeHostHome(t, "") // no ~/.claude
	creds := claudeCredentials(t, engine.AuthLogin)
	for name, axes := range map[string]launch.Axes{
		"host":      {},
		"container": {Workspace: WorkspaceShared, Runtime: RuntimeContainerRootless},
	} {
		s, err := NewSpec(axes, claudeEngine(t)).Project(t.TempDir()).
			Session(harpA, sessionDir(home, harpA), SessionState{Harp: harpA}).Credentials(creds).Build()
		require.NoError(t, err)
		_, err = Prepare(context.Background(), s)
		require.ErrorIs(t, err, engine.ErrNoCredential, name)
		fix, ok := clifmt.RemedyOf(err)
		require.True(t, ok, name)
		assert.Contains(t, fix, filepath.Join(home, ".claude"), "%s: the remedy names the missing dir", name)
		assert.Contains(t, fix, "auth: token", name)
		assert.NoDirExists(t, sessionDir(home, harpA), "%s: refused before anything was created", name)
	}
}
