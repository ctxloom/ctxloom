package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// forgeUndetectableURL cannot be parsed as a URL at all (a raw control
// character), so remote.DetectForge rejects it and no clone is ever attempted.
// It is how these tests exercise the refresh entry points offline: reaching
// UpdateRepo would mean a real git fetch.
const forgeUndetectableURL = "https://github.com/o/\x7frepo"

// Both refresh entry points share one per-repo body, so this pins the
// behaviour they must keep identical: a repository whose forge cannot be
// detected is skipped outright — no clone directory, no fetch, no crash.
// Fault tolerance here is load-bearing: `remote update` reports staleness, and
// a bad entry must never take the whole check down with it.
func TestRefreshRemoteClone_SkipsUndetectableForge(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppDir: appDir})

	refreshRemoteClone(context.Background(), cfg, forgeUndetectableURL)

	entries, err := os.ReadDir(filepath.Join(appDir, "cache", "repos"))
	if err == nil {
		assert.Empty(t, entries, "an undetectable forge must not produce a clone")
	}
}

// refreshRemoteRepos adds only the batch concerns on top of that shared body:
// entries with no locked SHA are not checked at all, an unparseable reference
// is skipped, and each unique repository is fetched at most once.
func TestRefreshRemoteRepos_SkipsUncheckableEntries(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppDir: appDir})

	lockfile := &remote.Lockfile{Bundles: map[string]remote.LockEntry{
		// No SHA: never pulled, so there is nothing to check for updates.
		"https://github.com/o/unpulled@bundles/x": {SHA: ""},
		// Unparseable reference: skipped, not fatal.
		"::::not-a-valid-reference": {SHA: "abc1234"},
		// Parses, but its forge cannot be detected.
		forgeUndetectableURL + "@bundles/x": {SHA: "abc1234"},
	}}

	refreshRemoteRepos(context.Background(), cfg, lockfile)

	entries, err := os.ReadDir(filepath.Join(appDir, "cache", "repos"))
	if err == nil {
		assert.Empty(t, entries, "no entry in this lockfile is fetchable, so nothing must be cloned")
	}
}
