// Registering a remote IS the consent to publish to it.
//
// A remote reaches the registry only because somebody typed `ctxloom remote
// create <name> <url>` and named it. Asking a second time, per remote, for a
// destination the user deliberately added answers a question they already
// answered — and the reflex it trains ("yes, that is the remote I just added")
// is worth less than the keystrokes it costs. The decision a human still makes
// is which remote to REGISTER; publish honors it.
//
// These tests are the ones that would go red if a grant, a ledger or a prompt
// came back: they publish to a registered remote from a session with nobody to
// ask and no record anywhere, and assert the BYTES ARRIVED. An exit-code
// assertion would pass against a gate that refused after the push, and against
// one that reported success having written nothing, so both ends read the
// destination back.
package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// registeredRemoteFixture is a publish manager holding exactly one registered
// remote and nothing else: no consent store, no asker, no terminal. That is
// the state of an agent, a CI job, an editor and a piped command, and it is
// the state in which publishing must work.
func registeredRemoteFixture(t *testing.T) *publishFixture {
	t.Helper()
	gitEnv(t)
	url, bare := bareRemote(t, "main")

	work := t.TempDir()
	local := filepath.Join(work, "mybundle.yaml")
	require.NoError(t, os.WriteFile(local, []byte("description: registered\n"), 0o644))

	fs := afero.NewOsFs()
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("shared", url))

	return &publishFixture{
		pm:        NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs)),
		localPath: local,
		remoteURL: url,
		bare:      bare,
	}
}

func (f *publishFixture) publishToShared(t *testing.T) (*PublishResult, error) {
	t.Helper()
	return f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
	})
}

// The whole claim, on a generic-git remote — the forge that carried the gate.
func TestPublish_ARegisteredRemoteNeedsNoSecondBlessing(t *testing.T) {
	f := registeredRemoteFixture(t)

	_, err := f.publishToShared(t)

	require.NoError(t, err, "a registered remote is a chosen remote; publishing to it is not gated")
	assert.Equal(t, "description: registered", f.remoteFile(t, "main", mybundleRemotePath),
		"the bytes reach the remote — success with nothing written is this project's characteristic bug")
}

// The second publish is the one a per-remote ledger would have made silent
// while the first refused. With no ledger, both are the same event: a
// republish must land the NEW bytes, not report success over the old ones.
func TestPublish_RepublishingToTheSameRemoteIsTheSameEvent(t *testing.T) {
	f := registeredRemoteFixture(t)

	_, err := f.publishToShared(t)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(f.localPath, []byte("description: registered v2\n"), 0o644))
	_, err = f.publishToShared(t)

	require.NoError(t, err)
	assert.Equal(t, "description: registered v2", f.remoteFile(t, "main", mybundleRemotePath))
}

// Publishing consults nothing under the user's home. A ledger reintroduced at
// the production path would be read here, and reading a missing one is exactly
// how the old gate produced a refusal — so an unwritable HOME must not change
// the outcome by a byte.
func TestPublish_ConsultsNothingUnderHome(t *testing.T) {
	f := registeredRemoteFixture(t)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "no-such-home"))

	_, err := f.publishToShared(t)

	require.NoError(t, err, "publish reads no per-user grant, so an absent home decides nothing")
	assert.Equal(t, "description: registered", f.remoteFile(t, "main", mybundleRemotePath))
}
