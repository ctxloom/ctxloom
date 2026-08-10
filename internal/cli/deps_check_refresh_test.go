package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// forgeUndetectableURL carries a port that is not a number, so url.Parse
// refuses it and remote.DetectForge returns an error rather than a forge — no
// clone is ever attempted. It is how these tests exercise the refresh entry
// points offline: reaching UpdateRepo would mean a real git fetch.
//
// The undetectability is STRUCTURAL, in the URL's authority, and must stay that
// way. This used to be spelled with a raw control character (DEL), which
// url.Parse also refuses — but a control character is no longer a durable way
// to make a string unparseable: references are normalised on ingest now
// (remote.NormalizeRef strips control characters, because a ref carrying one
// forges the countersign frame and can repaint a reviewer's terminal). Once the
// lockfile key below started being cleaned, this URL became the perfectly valid
// "https://github.com/o/repo", DetectForge succeeded, and the test attempted a
// real clone — which escaped t.TempDir() and wrote into the source tree. An
// invalid port survives normalisation byte for byte.
const forgeUndetectableURL = "https://github.com:zzz/o/repo"

// requireDetectForgeStillFails pins these tests' PREMISE rather than assuming
// it. The premise silently rotted once already: the constant above stopped
// being undetectable, and every assertion in this file kept passing while the
// behaviour under test was no longer being exercised at all. Asserting the
// premise is what turns that class of rot into a failure here, next to the
// comment that explains it, instead of into a stray directory found by a
// repo-wide guard.
func requireDetectForgeStillFails(t *testing.T) {
	t.Helper()
	_, _, err := remote.DetectForge(forgeUndetectableURL)
	require.Error(t, err,
		"forgeUndetectableURL must stay undetectable, or these tests attempt a real clone")
}

// requireNoClones asserts that nothing was cloned — the invariant both tests
// below actually claim, which is "no clone directory", not "no clone directory
// in one particular place".
//
// It checks TWO locations, and the second is the load-bearing one:
//
//   - Under appDir, where a correctly configured clone would land. Absent and
//     empty are both acceptable; anything else fails. This used to read
//     `if err == nil { assert.Empty(...) }`, which skipped the assertion
//     entirely whenever the directory did not exist.
//   - In the process's working directory. Absence under appDir is ALSO what an
//     escaped clone looks like from appDir's point of view, so the appDir check
//     alone stays vacuous in exactly the case that matters. When this file's
//     premise rotted, a real clone ran and landed in a RELATIVE ".ctxloom" —
//     operations.getBaseDir falls back to that literal when the config carries
//     no app paths — writing into the source tree while both tests reported
//     success. Only a repo-wide post-run guard noticed.
//
// The working-directory check is scoped to what this call did: it is skipped if
// the directory already existed on entry, so it attributes only its own damage.
func requireNoClones(t *testing.T, appDir, msg string, cwdDirtyBefore bool) {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(appDir, "cache", "repos"))
	if !errors.Is(err, os.ErrNotExist) {
		require.NoError(t, err, "the clone cache must be absent or readable, not broken")
		assert.Empty(t, entries, msg)
	}

	if cwdDirtyBefore {
		return // not ours to attribute
	}
	_, err = os.Stat(".ctxloom")
	assert.True(t, errors.Is(err, os.ErrNotExist),
		"%s — and a clone escaped into the working directory (./.ctxloom), which is the source tree", msg)
}

// cwdAlreadyDirty reports whether a ./.ctxloom exists before a test runs, so
// requireNoClones only ever blames a test for what that test created.
func cwdAlreadyDirty() bool {
	_, err := os.Stat(".ctxloom")
	return err == nil
}

// Both refresh entry points share one per-repo body, so this pins the
// behaviour they must keep identical: a repository whose forge cannot be
// detected is skipped outright — no clone directory, no fetch, no crash.
// Fault tolerance here is load-bearing: `deps check` reports staleness, and
// a bad entry must never take the whole check down with it.
func TestRefreshRemoteClone_SkipsUndetectableForge(t *testing.T) {
	requireDetectForgeStillFails(t)
	dirty := cwdAlreadyDirty()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppDir: appDir})

	refreshRemoteClone(context.Background(), cfg, forgeUndetectableURL)

	requireNoClones(t, appDir, "an undetectable forge must not produce a clone", dirty)
}

// refreshRemoteRepos adds only the batch concerns on top of that shared body:
// entries with no locked SHA are not checked at all, an unparseable reference
// is skipped, and each unique repository is fetched at most once.
func TestRefreshRemoteRepos_SkipsUncheckableEntries(t *testing.T) {
	requireDetectForgeStillFails(t)
	dirty := cwdAlreadyDirty()

	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppDir: appDir})

	lockfile := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
		// No SHA: never pulled, so there is nothing to check for updates.
		"https://github.com/o/unpulled@bundles/x": {SHA: ""},
		// Unparseable reference: skipped, not fatal.
		"::::not-a-valid-reference": {SHA: "abc1234"},
		// Parses as a reference, but its forge cannot be detected.
		forgeUndetectableURL + "@bundles/x": {SHA: "abc1234"},
	}}

	refreshRemoteRepos(context.Background(), cfg, lockfile)

	requireNoClones(t, appDir, "no entry in this lockfile is fetchable, so nothing must be cloned", dirty)
}

// TestForgeUndetectableURL_SurvivesRefNormalisation is the direct regression
// pin for how this file broke: the lockfile key above is a REFERENCE, and
// reference parsing normalises. If a future normalisation step rewrites this
// URL into something detectable, the tests above stop testing anything and
// start cloning — so assert that the ref path hands DetectForge the same bytes
// the constant declares.
func TestForgeUndetectableURL_SurvivesRefNormalisation(t *testing.T) {
	assert.Equal(t, forgeUndetectableURL, remote.NormalizeRef(forgeUndetectableURL),
		"normalisation must not rewrite this URL into a detectable one")

	parsed, err := remote.ParseReference(forgeUndetectableURL + "@bundles/x")
	require.NoError(t, err, "it must still parse as a reference, or the lockfile entry is skipped for the wrong reason")
	assert.Equal(t, forgeUndetectableURL, parsed.URL)
}
