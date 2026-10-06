package remote

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const verifyKey = "ctxloom+git://github.com/trent/atelier//bundles/atelier"

// acceptAll is a verifier that admits every tree.
func acceptAll(context.Context, map[string]TreeFile, string, string, string) (Verified, error) {
	return Verified{}, nil
}

type verifyEnv struct {
	lm         *LockfileManager
	checkedOut []string
}

// verifyPuller builds a Puller over an in-memory lockfile that already pins
// verifyKey at prior (skipped when prior.SHA is ""), with verifier tv.
func verifyPuller(t *testing.T, prior LockEntry, tv TreeVerifyFunc) (*Puller, *verifyEnv) {
	t.Helper()
	env := &verifyEnv{lm: NewLockfileManager("/test", WithLockfileFS(afero.NewMemMapFs()))}
	if prior.SHA != "" {
		lock, err := env.lm.Load()
		require.NoError(t, err)
		lock.AddEntry(ItemTypeBundle, verifyKey, prior)
		require.NoError(t, env.lm.Save(lock))
	}
	opts := []PullerOption{
		WithLockfileManager(env.lm),
		WithTreeInstaller(func(_ context.Context, _, sha, subpath, worktreeDir string) (string, error) {
			env.checkedOut = append(env.checkedOut, sha)
			return filepath.Join(worktreeDir, filepath.FromSlash(subpath)), nil
		}),
	}
	if tv != nil {
		opts = append(opts, WithTreeVerifier(tv))
	}
	return NewPuller(nil, AuthConfig{}, opts...), env
}

func verifyItem(t *testing.T, sha string) *fetchedItem {
	t.Helper()
	return &fetchedItem{
		rem:       &Remote{URL: "https://github.com/trent/atelier"},
		localName: verifyKey,
		sha:       sha,
		treeRoot:  treeRef(t).TreeRepoPath(),
		tree:      map[string]TreeFile{BundleManifestName: {Data: []byte("version: 1.0.0\n")}},
	}
}

func (e *verifyEnv) entry(t *testing.T) LockEntry {
	t.Helper()
	lock, err := e.lm.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(ItemTypeBundle, verifyKey)
	require.True(t, ok)
	return entry
}

const (
	oldSHA = "1111111111111111111111111111111111111111"
	newSHA = "2222222222222222222222222222222222222222"
)

func pullOpts() PullOptions {
	return PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test"}
}

func TestInstallPulledItem_RefusesWithoutAVerifierRatherThanPinningUnverifiedContent(t *testing.T) {
	p, env := verifyPuller(t, LockEntry{}, nil)
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(), verifyItem(t, newSHA))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verifier")
	assert.Empty(t, env.checkedOut)
}

func TestInstallPulledItem_AVerifierRefusalRefusesTheInstall(t *testing.T) {
	bad := func(context.Context, map[string]TreeFile, string, string, string) (Verified, error) {
		return Verified{}, errors.New("tampered: signed as other, served as atelier")
	}
	p, env := verifyPuller(t, LockEntry{}, bad)
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(), verifyItem(t, newSHA))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tampered")
	assert.Empty(t, env.checkedOut)
	lock, lerr := env.lm.Load()
	require.NoError(t, lerr)
	_, pinned := lock.GetEntry(ItemTypeBundle, verifyKey)
	assert.False(t, pinned)
}

// A pull re-records an existing pin at the commit it resolved and carries the
// hold with it: only `deps hold`/`unhold` changes Held.
func TestInstallPulledItem_CarriesTheHoldForward(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", Held: true}
	p, env := verifyPuller(t, prior, acceptAll)
	res, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(), verifyItem(t, oldSHA))
	require.NoError(t, err)
	assert.True(t, res.Reinstalled)
	got := env.entry(t)
	assert.Equal(t, oldSHA, got.SHA)
	assert.True(t, got.Held)
}
