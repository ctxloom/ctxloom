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

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// unconfirmedFixture is newPublishFixture without the pre-recorded
// confirmation, plus a counting asker the test can attach.
type unconfirmedFixture struct {
	*publishFixture
	store *PublishRemoteStore
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

	confirmPath := filepath.Join(work, "publish_remotes.yaml")
	store := NewPublishRemoteStore(confirmPath, fs)

	f := &unconfirmedFixture{store: store}
	all := append([]PublishManagerOption{WithPublishFS(fs), WithPublishRemoteStore(store)}, opts...)
	f.publishFixture = &publishFixture{
		pm:          NewPublishManager(registry, AuthConfig{}, all...),
		localPath:   local,
		remoteURL:   url,
		bare:        bare,
		confirmPath: confirmPath,
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

// confirmed reports what the store says about a URL, as the gate reads it.
func confirmedIn(t *testing.T, store *PublishRemoteStore, url string) bool {
	t.Helper()
	rec, found, err := store.Lookup(NewPublishRemoteKey(url))
	require.NoError(t, err)
	return found && rec.Approved
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
	assert.Contains(t, err.Error(), "ctxloom trust publish allow",
		"the refusal must name a command a CI job or an agent host can actually run")
	assert.Contains(t, err.Error(), f.confirmPath, "the refusal must say where the answer is recorded")

	// Nothing was pushed and nothing was recorded.
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)
	assert.False(t, confirmedIn(t, f.store, f.remoteURL), "a refusal must not record a confirmation")
}

// The first publish asks; the second, to the SAME remote, is silent.
func TestPublishConfirm_AsksOncePerRemoteThenStaysSilent(t *testing.T) {
	var f *unconfirmedFixture
	ask := func(_ context.Context, k PublishRemoteKey) (bool, bool, error) {
		f.asked = append(f.asked, k.URL)
		return true, true, nil
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

// "No" refuses and publishes nothing. It IS recorded — that is what makes it
// different from "nobody could be asked" — and the refusal says how to undo it
// rather than pretending nothing is there.
func TestPublishConfirm_DeclinedPublishesNothingAndIsRecordedAsDeclined(t *testing.T) {
	asked := 0
	ask := func(context.Context, PublishRemoteKey) (bool, bool, error) { asked++; return false, true, nil }
	f := newUnconfirmedFixture(t, WithRemoteAsk(ask))

	_, err := f.publish(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), f.remoteURL)
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)
	assert.False(t, confirmedIn(t, f.store, f.remoteURL))
	assert.Equal(t, 1, asked)

	// The recorded "no" answers on its own the next time — a declined remote
	// is a decision, not an absence, and must not re-ask on every attempt.
	_, err = f.publish(t)
	require.Error(t, err)
	assert.Equal(t, 1, asked, "a recorded decline must not be re-asked")
	assert.Contains(t, err.Error(), "recorded as declined",
		"a decline must not be reported as if nobody had ever been asked")
	assert.Contains(t, err.Error(), "trust publish forget", "and must say how to undo it")
}

// A question that could not be PUT — a closed stdin behind a callback — is not
// an answer, is never an affirmative, and is not recorded as a decline.
func TestPublishConfirm_AQuestionThatCameBackEmptyIsNotADecline(t *testing.T) {
	asked := 0
	ask := func(context.Context, PublishRemoteKey) (bool, bool, error) { asked++; return false, false, nil }
	f := newUnconfirmedFixture(t, WithRemoteAsk(ask))

	_, err := f.publish(t)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "never been confirmed",
		"nothing came back, so nobody was asked — not 'you declined'")
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)

	recs, lerr := f.store.List()
	require.NoError(t, lerr)
	assert.Empty(t, recs, "a transient non-answer must not harden into a recorded refusal")
	assert.Equal(t, 1, asked)
}

// A TYPO produces a different URL, and therefore a fresh question. This is the
// mistake the whole mechanism exists to catch, so it is pinned directly:
// confirming one remote must not silently authorise a neighbouring one.
func TestPublishConfirm_ATypoIsADifferentRemoteAndAsksAgain(t *testing.T) {
	store := NewPublishRemoteStore(filepath.Join(t.TempDir(), "publish_remotes.yaml"), afero.NewOsFs())
	_, err := store.Set(NewPublishRemoteKey("file:///srv/bundles.git"), true)
	require.NoError(t, err)
	assert.True(t, confirmedIn(t, store, "file:///srv/bundles.git"))

	for _, typo := range []string{
		"file:///srv/bundle.git",
		"file:///srv/bundles",
		"file:///srv/other/bundles.git",
		"ssh://git@evil.example.com/srv/bundles.git",
	} {
		assert.False(t, confirmedIn(t, store, typo),
			"%s is a different destination and must be confirmed on its own", typo)
	}
}

// One repository reached by two transport spellings is ONE destination: the
// store keys on the same normalized identity that keys the trust namespace and
// lockfile entries, so confirming the ssh spelling does not re-ask for https.
func TestPublishConfirm_OneRepositoryIsOneConfirmationAcrossSpellings(t *testing.T) {
	store := NewPublishRemoteStore(filepath.Join(t.TempDir(), "publish_remotes.yaml"), afero.NewOsFs())
	_, err := store.Set(NewPublishRemoteKey("git@git.example.com:team/bundles.git"), true)
	require.NoError(t, err)

	for _, spelling := range []string{
		"git@git.example.com:team/bundles.git",
		"https://git.example.com/team/bundles",
		"https://git.example.com/team/bundles.git",
	} {
		assert.True(t, confirmedIn(t, store, spelling),
			"%s names the same repository that was confirmed", spelling)
	}
}

// githubFixture is a GitHub-forge publish against mock transports, so the two
// tests below differ in exactly one thing: whether the destination is a
// confirmed publish remote. Same fixture shape both ways, which is what makes
// the negative assertion capable of failing.
type githubFixture struct {
	pm    *PublishManager
	mp    *mockPublisher
	url   string
	store *PublishRemoteStore
}

func newGitHubFixture(t *testing.T, opts ...PublishManagerOption) *githubFixture {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/local", 0o755))
	require.NoError(t, afero.WriteFile(fs, "/local/mybundle.yaml", []byte("description: gh\n"), 0o644))

	const url = "https://github.com/alice/ctxloom"
	registry, err := NewRegistry("", WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("alice", url))

	mp := newMockPublisher()
	mf := newMockFetcher()
	mf.defaultBranch = "main"
	store := NewPublishRemoteStore(filepath.Join(t.TempDir(), "publish_remotes.yaml"), fs)

	all := append([]PublishManagerOption{
		WithPublishFS(fs),
		WithPublisherFactory(mockPublisherFactory(mp)),
		WithPublishFetcherFactory(mockFetcherFactory(mf)),
		WithPublishRemoteStore(store),
	}, opts...)
	return &githubFixture{pm: NewPublishManager(registry, AuthConfig{}, all...), mp: mp, url: url, store: store}
}

func (f *githubFixture) publish(t *testing.T) error {
	t.Helper()
	_, err := f.pm.Publish(context.Background(), "/local/mybundle.yaml", "alice", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
	})
	return err
}

// TRUSTED SIDE. A GitHub remote the human confirmed publishes, and the file
// lands at the destination. This is the test that proves the negative below is
// capable of failing: same fixture, one record different.
func TestPublishConfirm_ConfirmedGitHubRemotePublishes(t *testing.T) {
	f := newGitHubFixture(t)
	_, err := f.store.Set(NewPublishRemoteKey(f.url), true)
	require.NoError(t, err)

	require.NoError(t, f.publish(t), "a confirmed GitHub remote must publish")
	assert.Contains(t, f.mp.createdFiles, mybundleRemotePath,
		"the trusted side must actually write the bundle, or the untrusted side proves nothing")
}

// UNTRUSTED SIDE. A GitHub remote nobody confirmed is refused, and NOTHING is
// pushed.
//
// A push token is not a confirmation. It answers "may this account write
// here", which is not the question the gate asks — "is this the destination
// you meant". A token with broad org rights authorises a typo'd repository
// just as readily as the intended one, and the forge API is precisely where
// that mistake becomes a public artifact.
func TestPublishConfirm_UnconfirmedGitHubRemoteRefusesAndPublishesNothing(t *testing.T) {
	f := newGitHubFixture(t) // no record, and no WithRemoteAsk: nobody to ask

	err := f.publish(t)
	require.Error(t, err, "an unconfirmed GitHub remote must be refused")
	assert.Contains(t, err.Error(), f.url, "the refusal must name the remote it refused")
	assert.Contains(t, err.Error(), "never been confirmed")
	// Asserted as substrings rather than one literal so this stays a test of
	// "the refusal is actionable" rather than a test of the current spelling.
	assert.Contains(t, err.Error(), "ctxloom",
		"the refusal must name a command a CI job or an agent host can actually run")
	assert.Contains(t, err.Error(), "trust", "and that command must be the one that grants the trust")

	assert.Empty(t, f.mp.createdFiles,
		"a refused publish must leave the destination untouched, not merely return an error")
	assert.False(t, confirmedIn(t, f.store, f.url), "a refusal must not record a confirmation")
}

// A store with no path is a MISCONFIGURATION (an unresolvable $HOME), not an
// empty store. Because filepath.Join("", x) == x, an unconfigured store that
// resolved relative to the process working directory would let a stray file at
// a repo root authorise a push. It must answer nothing and refuse every write.
func TestPublishRemoteStore_UnconfiguredStoreRefusesRatherThanReadingTheWorkingDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	// A file that WOULD be a confirmation if the store resolved "" to ".".
	require.NoError(t, afero.WriteFile(fs, "publish_remotes.yaml", []byte(
		"version: 1\nrecords:\n  - key:\n      url: file:///srv/bundles.git\n"+
			"      identity: file:///srv/bundles.git\n    approved: true\n"), 0o600))

	store := NewPublishRemoteStore("", fs)

	_, _, err := store.Lookup(NewPublishRemoteKey("file:///srv/bundles.git"))
	require.Error(t, err, "an unconfigured store must not answer from the working directory")

	_, serr := store.Set(NewPublishRemoteKey("file:///srv/bundles.git"), true)
	require.Error(t, serr, "an unconfigured store must not write into the working directory")

	// And the gate built over it REFUSES rather than admitting on the planted file.
	pm := &PublishManager{confirmed: store}
	gerr := pm.authorizeRemote(context.Background(), "file:///srv/bundles.git")
	require.Error(t, gerr)
	assert.Contains(t, gerr.Error(), "refusing to publish")
}

// A store whose file does not exist yet is the normal fresh state: nothing
// confirmed, no error. Recording creates it, and the record is readable by a
// human with `cat` — it names the URL it covers and when it was answered.
func TestPublishRemoteStore_AbsentFileIsEmptyAndSetCreatesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never", "created", "publish_remotes.yaml")
	store := NewPublishRemoteStore(path, afero.NewOsFs())

	recs, err := store.List()
	require.NoError(t, err)
	assert.Empty(t, recs)
	assert.False(t, confirmedIn(t, store, "file:///srv/bundles.git"))

	rec, serr := store.Set(NewPublishRemoteKey("file:///srv/bundles.git"), true)
	require.NoError(t, serr)
	assert.Equal(t, "file:///srv/bundles.git", rec.Key.URL)
	assert.True(t, confirmedIn(t, store, "file:///srv/bundles.git"))

	body, rerr := os.ReadFile(path)
	require.NoError(t, rerr)
	assert.Contains(t, string(body), "file:///srv/bundles.git")
	assert.Contains(t, string(body), "recorded_at")
}

// The PROVISIONING verbs — list, set, forget — are what a CI job and an agent
// host have instead of "run it once interactively", which they cannot do.
// Asserted on the DECISION each verb produces, not on a nil error.
func TestPublishRemoteStore_ListSetForgetAreTheProvisioningPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publish_remotes.yaml")
	store := NewPublishRemoteStore(path, afero.NewOsFs())
	url := "https://git.example.com/team/bundles"

	// Nothing recorded: a non-interactive gate refuses.
	pm := &PublishManager{confirmed: store}
	require.Error(t, pm.authorizeRemote(context.Background(), url))

	// Provisioned by hand: the same gate now admits, with nobody asked.
	_, err := store.Set(NewPublishRemoteKey(url), true)
	require.NoError(t, err)
	require.NoError(t, pm.authorizeRemote(context.Background(), url),
		"a confirmation recorded out of band must satisfy the gate exactly as a prompt would")

	recs, lerr := store.List()
	require.NoError(t, lerr)
	require.Len(t, recs, 1)
	assert.Equal(t, url, recs[0].Key.URL)
	assert.True(t, recs[0].Approved)

	// Revoked: back to refusing, and the count is reported.
	n, ferr := store.Forget(NewPublishRemoteKey(url))
	require.NoError(t, ferr)
	assert.Equal(t, 1, n)
	require.Error(t, pm.authorizeRemote(context.Background(), url),
		"a forgotten confirmation must stop admitting")

	n, ferr = store.Forget(NewPublishRemoteKey(url))
	require.NoError(t, ferr)
	assert.Equal(t, 0, n, "forgetting nothing must report zero, never success-with-no-effect")
}

// A recorded DECLINE is honored by the gate on its own, without asking, and is
// reported as a decline rather than as an absence.
func TestPublishRemoteStore_ARecordedDeclineRefusesWithoutAsking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publish_remotes.yaml")
	store := NewPublishRemoteStore(path, afero.NewOsFs())
	url := "https://git.example.com/team/bundles"
	_, err := store.Set(NewPublishRemoteKey(url), false)
	require.NoError(t, err)

	asked := 0
	pm := &PublishManager{
		confirmed: store,
		ask: func(context.Context, PublishRemoteKey) (bool, bool, error) {
			asked++
			return true, true, nil
		},
	}
	gerr := pm.authorizeRemote(context.Background(), url)
	require.Error(t, gerr)
	assert.Equal(t, 0, asked, "a recorded decline must not be re-put to the human")
	assert.Contains(t, gerr.Error(), "recorded as declined")
}

// An UNREADABLE store refuses, and says so as a fault rather than as "you never
// confirmed it" — re-asking about a remote already approved is what teaches
// people to answer the prompt on reflex.
func TestPublishRemoteStore_AnUnreadableStoreRefusesAsAFault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "publish_remotes.yaml")
	require.NoError(t, os.WriteFile(path, []byte("version: 99\nrecords: []\n"), 0o600))
	store := NewPublishRemoteStore(path, afero.NewOsFs())

	asked := 0
	pm := &PublishManager{
		confirmed: store,
		ask: func(context.Context, PublishRemoteKey) (bool, bool, error) {
			asked++
			return true, true, nil
		},
	}
	gerr := pm.authorizeRemote(context.Background(), "file:///srv/bundles.git")
	require.Error(t, gerr)
	assert.Equal(t, 0, asked, "an unreadable store must not fall through to a prompt")
	assert.Contains(t, gerr.Error(), "cannot tell whether")
}
