package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
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
		out[read.DisplayName()] = read.Bundle
	}
	return out
}

// TestLoadRemoteBundleSeed_FullLoad and its seedSourceRepo fixture were
// REMOVED, not reshaped: they drove remoteBundleReaders' remote.LoadAllBytes
// path (a bare "<name>.yaml" committed straight at the format-v2 prefix, then
// cloned and read with no tree fetcher wired). Format v2 publishes ONLY
// trees, and remote.BundleReader.ReadBundleBytes/ReadFromTree has no
// single-file branch left at all — every entry it is handed refuses with "no
// tree read surface wired" (see remote.ErrTreeBundleUnreadable), unconditionally.
// The ONLY path that resolves a real v2 lockfile entry today is
// treeBundleReaders, reading the tree `deps pull` already installed to cache —
// there is no "read straight from the pinned clone, no prior pull" capability
// left to test here.
//
// The invariants this test pinned split three ways:
//   - "a bundle signed/unsigned/tampered loads correctly" — already covered,
//     via the LIVE mechanism, by tree_bundles_test.go's TestLoadTreeBundle_*
//     tests (stageInstalledTree + treeBundleReader).
//   - "a malformed bundle among several is skipped, not fatal, and the good
//     one still loads" — had no equivalent tree-path test, so it was ADDED as
//     TestTreeBundleReaders_MalformedEntryIsSkippedGoodOneStillLoads in
//     tree_bundles_test.go.
//   - "the seed is canonical-keyed, never a short key" and "a document's
//     synthetic <remote>:name@sha Path" are DOCUMENT-ONLY facts (see
//     documentTree in config.go) that no longer apply to any bundle format v2
//     can actually publish; dropped rather than asserted about a tree bundle,
//     whose Path is its real installed directory (see
//     TestLoadTreeBundle_PathResolvesToTheInstalledDirectorySoSkillsCanLoad).

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
