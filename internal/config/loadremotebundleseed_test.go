package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// remoteBundleSeed reads every pinned-remote reader the Config builds and
// returns what they reported, keyed by canonical ref.
//
// It is the honest replacement for the retired seed map: the same content, the
// same identities, now established by the readers that read the bytes. A reader
// that ERRORS (a tampered tree, a document that will not parse) contributes
// nothing and is not a fatal condition here, exactly as the seed map's failure
// path behaved.
func remoteBundleSeed(t *testing.T, cfg *Config) map[string]*bundles.Bundle {
	t.Helper()
	readers := cfg.remoteBundleReaders()
	if len(readers) == 0 {
		return nil
	}
	// Through a LOADER, not the readers directly: what a session can address is
	// the loader's answer, and the withholds that matter (a remote signature
	// that does not cover its bytes) are applied there. Reading the readers raw
	// would assert on facts nobody consumes.
	out := map[string]*bundles.Bundle{}
	for _, read := range bundles.NewLoader(readers...).Reads() {
		out[read.Ref()] = read.Bundle
	}
	return out
}

// seedSourceRepo creates a real git repo shipping a valid bundle and a
// malformed one, returning its path and HEAD SHA. The full-load path of
// loadRemoteBundleSeed clones this over file:// and reads each locked bundle at
// that SHA.
func seedSourceRepo(t *testing.T) (repoDir, sha string) {
	t.Helper()
	repoDir = filepath.Join(t.TempDir(), "source")
	repo, err := git.PlainInit(repoDir, false)
	require.NoError(t, err)
	wt, err := repo.Worktree()
	require.NoError(t, err)

	bundleDir := filepath.Join(repoDir, ".ctxloom", "content", "bundles")
	require.NoError(t, os.MkdirAll(bundleDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "good.yaml"),
		[]byte("version: v1\ndescription: a good bundle\n"), 0644))
	// A leading tab is invalid YAML, so ParseBundle rejects this one.
	require.NoError(t, os.WriteFile(filepath.Join(bundleDir, "bad.yaml"),
		[]byte("\tnot: valid yaml\n"), 0644))

	for _, f := range []string{".ctxloom/content/bundles/good.yaml", ".ctxloom/content/bundles/bad.yaml"} {
		_, err = wt.Add(f)
		require.NoError(t, err)
	}
	commit, err := wt.Commit("seed", &git.CommitOptions{
		Author: &object.Signature{Name: "test", Email: "test@test.com", When: time.Now()},
	})
	require.NoError(t, err)
	return repoDir, commit.String()
}

// TestLoadRemoteBundleSeed_FullLoad covers the materialization path past the
// guard branches: a non-empty lockfile drives a real clone of the locked repo,
// each bundle is read at its SHA and parsed, a malformed bundle is skipped, and
// each loaded bundle carries its name and synthetic "<remote>:name@sha" path.
func TestLoadRemoteBundleSeed_FullLoad(t *testing.T) {
	testsupport.Isolate(t)
	repoDir, sha := seedSourceRepo(t)
	repoURL := "file://" + repoDir

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))

	lm := remote.NewLockfileManager(appDir)
	lock, err := lm.Load()
	require.NoError(t, err)
	entry := remote.LockEntry{SHA: sha, URL: repoURL, FetchedAt: time.Now().UTC()}
	lock.AddEntry(remote.ItemTypeBundle, repoURL+"@bundles/good", entry)
	lock.AddEntry(remote.ItemTypeBundle, repoURL+"@bundles/bad", entry)
	require.NoError(t, lm.Save(lock))

	cfg := &Config{appPaths: []string{appDir}}
	seed := remoteBundleSeed(t, cfg)

	require.NotNil(t, seed, "a populated lockfile must materialize a seed")
	// The seed is keyed by the canonical ref — the sole resolution identity.
	canonical := repoURL + "@bundles/good"
	good, ok := seed[canonical]
	require.True(t, ok, "the valid bundle is loaded and keyed by its canonical ref")
	assert.Equal(t, canonical, good.Name)
	assert.Equal(t, "v1", good.Version, "bundle content is parsed at the locked SHA")
	assert.Equal(t, "<remote>:"+canonical+"@"+sha, good.Path,
		"the synthetic path names the ref AND the revision its bytes were pinned at")

	_, badLoaded := seed[repoURL+"@bundles/bad"]
	assert.False(t, badLoaded, "a malformed bundle is skipped, not fatal")
	_, shortKeyed := seed["src/good"]
	assert.False(t, shortKeyed, "the seed is canonical-keyed only — no short keys")
}

// TestLoadRemoteBundleSeed_RegistryErrorWarnsNotSilent proves a
// genuine registry-open failure (as opposed to "no remotes.yaml yet", which
// stays silent — see remote.NewRegistry's own os.IsNotExist carve-out) is
// diagnosed rather than indistinguishable from "no remotes configured".
func TestLoadRemoteBundleSeed_RegistryErrorWarnsNotSilent(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	// A directory where remotes.yaml is expected: ReadFile fails with a
	// real (non-ENOENT) error.
	require.NoError(t, os.MkdirAll(filepath.Join(appDir, "remotes.yaml"), 0755))

	var buf syncBuffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	cfg := &Config{appPaths: []string{appDir}}
	seed := remoteBundleSeed(t, cfg)

	assert.Nil(t, seed)
	assert.Contains(t, buf.String(), "remotes registry",
		"a real registry-open failure must be diagnosed, not silently indistinguishable from \"no remotes\"")
}

// TestLoadRemoteBundleSeed_LockfileParseErrorWarnsNotSilent is the
// lockfile half: an unparseable lock.yaml is a real error (contrast the
// ordinary missing-file case, which LockfileManager.Load itself already
// degrades to an empty, no-error lockfile).
func TestLoadRemoteBundleSeed_LockfileParseErrorWarnsNotSilent(t *testing.T) {
	testsupport.Isolate(t)
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "lock.yaml"), []byte("\tnot: valid yaml\n"), 0644))

	var buf syncBuffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	cfg := &Config{appPaths: []string{appDir}}
	seed := remoteBundleSeed(t, cfg)

	assert.Nil(t, seed)
	assert.Contains(t, buf.String(), "lockfile",
		"a real lockfile parse failure must be diagnosed, not silently indistinguishable from \"nothing pinned\"")
}
