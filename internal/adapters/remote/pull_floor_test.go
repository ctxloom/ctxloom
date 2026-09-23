package remote

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/release"
)

const floorKey = "https://github.com/trent/atelier@bundles/atelier"

// signedAs is a verifier that reports every tree as signed by pub@example.test
// at version (or unattested when version is "").
func signedAs(version string) TreeVerifyFunc {
	return func(context.Context, map[string]TreeFile, string, string, string) (Verified, error) {
		if version == "" {
			return Verified{}, nil
		}
		return Verified{
			Release:   release.Release{Name: "atelier", Version: semver.MustParse(version)},
			Publisher: "pub@example.test",
		}, nil
	}
}

type floorEnv struct {
	lm         *LockfileManager
	checkedOut []string
}

// floorPuller builds a Puller over an in-memory lockfile that already pins
// floorKey at prior (skipped when prior.SHA is ""), with verifier tv.
func floorPuller(t *testing.T, prior LockEntry, tv TreeVerifyFunc) (*Puller, *floorEnv) {
	t.Helper()
	env := &floorEnv{lm: NewLockfileManager("/test", WithLockfileFS(afero.NewMemMapFs()))}
	if prior.SHA != "" {
		lock, err := env.lm.Load()
		require.NoError(t, err)
		lock.AddEntry(ItemTypeBundle, floorKey, prior)
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

func floorItem(t *testing.T, sha string) *fetchedItem {
	t.Helper()
	return &fetchedItem{
		rem:       &Remote{URL: "https://github.com/trent/atelier"},
		localName: floorKey,
		sha:       sha,
		treeRoot:  treeRef(t).TreeRepoPath(),
		tree:      map[string]TreeFile{BundleManifestName: {Data: []byte("version: 1.0.0\n")}},
	}
}

func (e *floorEnv) entry(t *testing.T) LockEntry {
	t.Helper()
	lock, err := e.lm.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(ItemTypeBundle, floorKey)
	require.True(t, ok)
	return entry
}

const (
	oldSHA = "1111111111111111111111111111111111111111"
	newSHA = "2222222222222222222222222222222222222222"
)

func pullOpts(out *bytes.Buffer) PullOptions {
	return PullOptions{ItemType: ItemTypeBundle, LocalDir: "/test", Stdout: out}
}

func TestInstallPulledItem_RefusesWithoutAVerifierRatherThanPinningUnverifiedContent(t *testing.T) {
	p, env := floorPuller(t, LockEntry{}, nil)
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "verifier")
	assert.Empty(t, env.checkedOut)
}

func TestInstallPulledItem_RecordsTheSignedVersionAndPublisher(t *testing.T) {
	p, env := floorPuller(t, LockEntry{}, signedAs("1.2.0"))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.NoError(t, err)
	got := env.entry(t)
	assert.Equal(t, "1.2.0", got.SignedVersion)
	assert.Equal(t, "pub@example.test", got.Publisher)
}

func TestInstallPulledItem_UnattestedRecordsNoFloor(t *testing.T) {
	p, env := floorPuller(t, LockEntry{}, signedAs(""))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.NoError(t, err)
	got := env.entry(t)
	assert.Empty(t, got.SignedVersion)
	assert.Empty(t, got.Publisher)
}

// Attack (a): whoever controls the repository moves the ref back to an older
// tree the publisher really did sign. Every signature verifies; only the floor
// refuses it — and it must refuse BEFORE the checkout moves or the pin is
// written.
func TestInstallPulledItem_ARollbackToAnOlderSignedReleaseIsRefused(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", SignedVersion: "1.2.0", Publisher: "pub@example.test"}
	p, env := floorPuller(t, prior, signedAs("1.1.0"))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.Error(t, err)
	assert.True(t, errors.Is(err, release.ErrRollback), "got %v", err)
	assert.Empty(t, env.checkedOut, "the checkout must not move onto a refused tree")
	got := env.entry(t)
	assert.Equal(t, oldSHA, got.SHA)
	assert.Equal(t, "1.2.0", got.SignedVersion)
}

func TestInstallPulledItem_StrippingTheSignatureDoesNotEscapeTheFloor(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", SignedVersion: "1.2.0", Publisher: "pub@example.test"}
	p, env := floorPuller(t, prior, signedAs(""))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	assert.True(t, errors.Is(err, release.ErrSignatureDowngrade), "got %v", err)
	assert.Empty(t, env.checkedOut)
	assert.Equal(t, oldSHA, env.entry(t).SHA)
}

func TestInstallPulledItem_TheSameSignedVersionIsAccepted(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", SignedVersion: "1.2.0", Publisher: "pub@example.test"}
	p, env := floorPuller(t, prior, signedAs("1.2.0"))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.NoError(t, err)
	assert.Equal(t, newSHA, env.entry(t).SHA)
}

func TestInstallPulledItem_AllowDowngradeSaysSoAndLowersTheFloor(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", SignedVersion: "1.2.0", Publisher: "pub@example.test"}
	p, env := floorPuller(t, prior, signedAs("1.1.0"))
	var out bytes.Buffer
	opts := pullOpts(&out)
	opts.AllowDowngrade = true
	_, err := p.installPulledItem(t.Context(), treeRef(t), opts, floorItem(t, newSHA))
	require.NoError(t, err)
	assert.Contains(t, out.String(), "downgrading "+floorKey+" from 1.2.0 to 1.1.0 at your request")
	got := env.entry(t)
	assert.Equal(t, newSHA, got.SHA)
	assert.Equal(t, "1.1.0", got.SignedVersion, "the lower version becomes the new floor")
}

func TestInstallPulledItem_AVerifierRefusalRefusesTheInstall(t *testing.T) {
	bad := func(context.Context, map[string]TreeFile, string, string, string) (Verified, error) {
		return Verified{}, errors.New("tampered: signed as other, served as atelier")
	}
	p, env := floorPuller(t, LockEntry{}, bad)
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tampered")
	assert.Empty(t, env.checkedOut)
	lock, lerr := env.lm.Load()
	require.NoError(t, lerr)
	_, pinned := lock.GetEntry(ItemTypeBundle, floorKey)
	assert.False(t, pinned)
}

func TestInstallPulledItem_AHoldKeepsItsFloor(t *testing.T) {
	prior := LockEntry{SHA: oldSHA, URL: "https://github.com/trent/atelier", Held: true, SignedVersion: "1.2.0", Publisher: "pub@example.test"}
	p, env := floorPuller(t, prior, signedAs("0.1.0"))
	_, err := p.installPulledItem(t.Context(), treeRef(t), pullOpts(&bytes.Buffer{}), floorItem(t, newSHA))
	require.NoError(t, err, "a held pin installs its own already-verified commit, not the fetched one")
	got := env.entry(t)
	assert.Equal(t, oldSHA, got.SHA)
	assert.Equal(t, "1.2.0", got.SignedVersion)
	assert.Equal(t, "pub@example.test", got.Publisher)
}
