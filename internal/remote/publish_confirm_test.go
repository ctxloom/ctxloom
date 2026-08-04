// Tests for the publish-remote confirmation: ask once per new remote, record
// the answer, and REFUSE — never prompt, never assume yes — when there is
// nobody to ask.
//
// Every "it refused" assertion also checks the destination is still untouched.
// A gate that returns an error after the push has already happened is not a
// gate, and an error return alone cannot tell the two apart.
package remote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testNow is the fixed instant recorded confirmations are stamped with, so a
// store's contents are reproducible.
func testNow() time.Time {
	return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
}

// unconfirmedFixture is newPublishFixture without the pre-recorded
// confirmation, plus a counting asker the test can attach.
type unconfirmedFixture struct {
	*publishFixture
	store *ConfirmedRemotes
	asked []string
}

func newUnconfirmedFixture(t *testing.T, opts ...PublishManagerOption) *unconfirmedFixture {
	t.Helper()
	gitEnv(t)
	url, bare := bareRemote(t, "main")

	work := t.TempDir()
	local := filepath.Join(work, "mybundle.yaml")
	require.NoError(t, os.WriteFile(local, []byte("description: guarded\n"), 0o644))

	fs := afero.NewOsFs()
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("shared", url))

	confirmDir := filepath.Join(work, "publish-remotes")
	store := NewConfirmedRemotes(confirmDir, fs)

	f := &unconfirmedFixture{store: store}
	all := append([]PublishManagerOption{WithPublishFS(fs), WithConfirmedRemotes(store)}, opts...)
	f.publishFixture = &publishFixture{
		pm:         NewPublishManager(registry, AuthConfig{}, all...),
		localPath:  local,
		remoteURL:  url,
		bare:       bare,
		confirmDir: confirmDir,
	}
	return f
}

func (f *unconfirmedFixture) publish(t *testing.T) (*PublishResult, error) {
	t.Helper()
	return f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
	})
}

// A session with no human attached — an agent, an MCP call, a CI job, a piped
// command — must REFUSE an unconfirmed remote. It must name the remote and say
// how to confirm it, and it must not have published anything.
func TestPublishConfirm_NonInteractiveRefusesAndPublishesNothing(t *testing.T) {
	f := newUnconfirmedFixture(t) // no WithRemoteAsk: nobody to ask

	_, err := f.publish(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), f.remoteURL, "the refusal must name the remote it refused")
	assert.Contains(t, err.Error(), "never been confirmed")
	assert.Contains(t, err.Error(), "interactive terminal", "the refusal must say how to confirm it")
	assert.Contains(t, err.Error(), f.confirmDir, "the refusal must say where the answer is recorded")

	// Nothing was pushed and nothing was recorded.
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)
	confirmed, cerr := f.store.Confirmed(f.remoteURL)
	require.NoError(t, cerr)
	assert.False(t, confirmed, "a refusal must not record a confirmation")
}

// The first publish asks; the second, to the SAME remote, is silent.
func TestPublishConfirm_AsksOncePerRemoteThenStaysSilent(t *testing.T) {
	var f *unconfirmedFixture
	ask := func(_ context.Context, url string) (bool, error) {
		f.asked = append(f.asked, url)
		return true, nil
	}
	f = newUnconfirmedFixture(t, WithRemoteAsk(ask))

	_, err := f.publish(t)
	require.NoError(t, err)
	require.Equal(t, []string{f.remoteURL}, f.asked, "the first publish to a new remote must ask")
	assert.Equal(t, "description: guarded", f.remoteFile(t, "main", mybundleRemotePath))

	// A routine republish must not ask again.
	require.NoError(t, os.WriteFile(f.localPath, []byte("description: guarded v2\n"), 0o644))
	_, err = f.publish(t)
	require.NoError(t, err)
	assert.Len(t, f.asked, 1, "a republish to an already-confirmed remote must be silent")
	assert.Equal(t, "description: guarded v2", f.remoteFile(t, "main", mybundleRemotePath))
}

// "No" refuses and publishes nothing — and does not record a confirmation, so
// the next attempt asks again rather than inheriting the refusal as an answer.
func TestPublishConfirm_DeclinedPublishesNothing(t *testing.T) {
	asked := 0
	ask := func(context.Context, string) (bool, error) { asked++; return false, nil }
	f := newUnconfirmedFixture(t, WithRemoteAsk(ask))

	_, err := f.publish(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), f.remoteURL)
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)

	confirmed, cerr := f.store.Confirmed(f.remoteURL)
	require.NoError(t, cerr)
	assert.False(t, confirmed)
	assert.Equal(t, 1, asked)
}

// A TYPO produces a different URL, and therefore a fresh question. This is the
// mistake the whole mechanism exists to catch, so it is pinned directly:
// confirming one remote must not silently authorise a neighbouring one.
func TestPublishConfirm_ATypoIsADifferentRemoteAndAsksAgain(t *testing.T) {
	store := NewConfirmedRemotes(t.TempDir(), afero.NewOsFs())
	require.NoError(t, store.Record("file:///srv/bundles.git", testNow()))

	confirmed, err := store.Confirmed("file:///srv/bundles.git")
	require.NoError(t, err)
	assert.True(t, confirmed)

	for _, typo := range []string{
		"file:///srv/bundle.git",
		"file:///srv/bundles",
		"file:///srv/other/bundles.git",
		"ssh://git@evil.example.com/srv/bundles.git",
	} {
		got, err := store.Confirmed(typo)
		require.NoError(t, err)
		assert.False(t, got, "%s is a different destination and must be confirmed on its own", typo)
	}
}

// One repository reached by two transport spellings is ONE destination: the
// store keys on the same normalized identity that keys the trust namespace and
// lockfile entries, so confirming the ssh spelling does not re-ask for https.
func TestPublishConfirm_OneRepositoryIsOneConfirmationAcrossSpellings(t *testing.T) {
	store := NewConfirmedRemotes(t.TempDir(), afero.NewOsFs())
	require.NoError(t, store.Record("git@git.example.com:team/bundles.git", testNow()))

	for _, spelling := range []string{
		"git@git.example.com:team/bundles.git",
		"https://git.example.com/team/bundles",
		"https://git.example.com/team/bundles.git",
	} {
		got, err := store.Confirmed(spelling)
		require.NoError(t, err)
		assert.True(t, got, "%s names the same repository that was confirmed", spelling)
	}
}

// GitHub publish is deliberately OUTSIDE this gate: it goes through the forge
// API with a token that must already carry push rights to that repository, and
// folding it in would change an existing, acceptance-covered path as a side
// effect of adding a new one. Pinned so widening the scope is a deliberate
// act with a failing test attached, not an accident.
func TestPublishConfirm_GitHubIsNotGated(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: gh\n"), 0o644))

	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", "https://github.com/alice/ctxloom"))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"

	// No asker and an UNCONFIGURED confirmation store: if GitHub were gated,
	// this would fail closed on the store rather than publish.
	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
		WithConfirmedRemotes(NewConfirmedRemotes("", fs)),
	)

	_, err = pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
	})
	require.NoError(t, err, "GitHub publish must be unchanged by the generic-git confirmation")
	assert.Contains(t, mp.createdFiles, mybundleRemotePath)
}

// A store with no directory is a MISCONFIGURATION (an unresolvable $HOME), not
// an empty store. Because a marker's mere existence is the confirmation, and
// filepath.Join("", x) == x, an unconfigured store that read paths relative to
// the process working directory would let a stray file at a repo root
// authorise a push. It must answer nothing and refuse every write instead.
func TestConfirmedRemotes_UnconfiguredStoreRefusesRatherThanReadingTheWorkingDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	// A file that WOULD be a confirmation if the store resolved "" to ".".
	require.NoError(t, afero.WriteFile(fs, confirmKey("file:///srv/bundles.git"), []byte("x"), 0o644))

	store := NewConfirmedRemotes("", fs)

	_, err := store.Confirmed("file:///srv/bundles.git")
	require.Error(t, err, "an unconfigured store must not answer from the working directory")

	require.Error(t, store.Record("file:///srv/bundles.git", testNow()),
		"an unconfigured store must not write into the working directory")
}

// A store whose directory does not exist yet is the normal fresh state:
// nothing confirmed, no error. Recording creates it.
func TestConfirmedRemotes_AbsentDirectoryIsEmptyAndRecordCreatesIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never", "created")
	store := NewConfirmedRemotes(dir, afero.NewOsFs())

	got, err := store.Confirmed("file:///srv/bundles.git")
	require.NoError(t, err)
	assert.False(t, got)

	require.NoError(t, store.Record("file:///srv/bundles.git", testNow()))
	got, err = store.Confirmed("file:///srv/bundles.git")
	require.NoError(t, err)
	assert.True(t, got)

	// The record is readable by a human with `cat`: it names the URL it
	// covers and when it was answered.
	body, err := os.ReadFile(filepath.Join(dir, confirmKey("file:///srv/bundles.git")))
	require.NoError(t, err)
	assert.Contains(t, string(body), "file:///srv/bundles.git")
	assert.Contains(t, string(body), "2026-08-04T12:00:00Z")
}
