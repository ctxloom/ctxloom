// Round-trip tests for publishing to a generic git remote. Every one of them
// drives the REAL git binary against a REAL bare repository over file:// and
// asserts what the BARE REPO holds afterwards — never that Publish returned a
// nil error.
//
// That distinction is the point of the file. ctxloom's characteristic bug is
// the silent no-op: exit 0, a success message, and zero bytes written. A
// publisher is exactly the shape that fails that way, so the assertions here
// read the destination's own tree (`git show <branch>:<path>`) and its own ref
// (`git rev-parse`), which is the only evidence a push happened.
package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gitEnv makes the test's git invocations — and, through
// gitutil.SanitizedEnviron, the PUBLISHER's — hermetic and able to commit at
// all: an identity from the environment, and no developer's global/system
// config leaking in (a global commit.gpgsign would otherwise fail every commit
// here for reasons that have nothing to do with publishing).
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_AUTHOR_NAME", "ctxloom test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@ctxloom.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "ctxloom test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@ctxloom.invalid")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
}

// gitRun runs a git command in dir and returns its trimmed stdout, failing the
// test on any error (with git's own output attached).
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %s: %s", strings.Join(args, " "), out)
	return strings.TrimSpace(string(out))
}

// gitFails runs a git command expected to FAIL, returning its output. Used to
// assert absence — that a ref or a path is genuinely not there.
func gitFails(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.Error(t, err, "git %s unexpectedly succeeded: %s", strings.Join(args, " "), out)
	return string(out)
}

// bareRemote creates a bare repository with one seed commit on branch, and
// returns its file:// URL and its on-disk path. The bare repo's HEAD names
// branch, so a clone with no branch pinned lands there — which is how the
// default-branch tests prove nothing is hardcoded to "main".
func bareRemote(t *testing.T, branch string) (url, bare string) {
	t.Helper()
	root := t.TempDir()
	bare = filepath.Join(root, "bundles.git")
	gitRun(t, root, "init", "--bare", "-b", branch, bare)

	seed := filepath.Join(root, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	gitRun(t, seed, "init", "-b", branch)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644))
	gitRun(t, seed, "add", "-A")
	gitRun(t, seed, "commit", "-m", "seed")
	gitRun(t, seed, "remote", "add", "origin", bare)
	gitRun(t, seed, "push", "origin", branch)

	return "file://" + bare, bare
}

// publishFixture wires a PublishManager over a real local file and a real
// file:// remote, with the remote ALREADY confirmed (the confirmation itself is
// exercised in publish_confirm_test.go).
type publishFixture struct {
	pm         *PublishManager
	localPath  string
	remoteURL  string
	bare       string
	confirmPath string
}

func newPublishFixture(t *testing.T, branch, body string) *publishFixture {
	t.Helper()
	gitEnv(t)
	url, bare := bareRemote(t, branch)

	work := t.TempDir()
	local := filepath.Join(work, "mybundle.yaml")
	require.NoError(t, os.WriteFile(local, []byte(body), 0o644))

	fs := afero.NewOsFs()
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("shared", url))

	confirmPath := filepath.Join(work, "publish_remotes.yaml")
	store := NewPublishRemoteStore(confirmPath, fs)
	_, serr := store.Set(NewPublishRemoteKey(url), true)
	require.NoError(t, serr)

	pm := NewPublishManager(registry, AuthConfig{},
		WithPublishFS(fs),
		WithPublishRemoteStore(store),
	)
	return &publishFixture{pm: pm, localPath: local, remoteURL: url, bare: bare, confirmPath: confirmPath}
}

// remoteFile reads a path out of the BARE repository at branch — the
// destination's own view, not the publisher's.
func (f *publishFixture) remoteFile(t *testing.T, branch, path string) string {
	t.Helper()
	return gitRun(t, f.bare, "show", branch+":"+path)
}

func TestGitPublisher_PublishLandsInTheBareRepository(t *testing.T) {
	body := "description: Test bundle\nfragments:\n  test:\n    content: hello\n"
	f := newPublishFixture(t, "main", body)

	result, err := f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
		Title:      "Add bundle mybundle",
	})
	require.NoError(t, err)

	// The BARE REPO holds the exact local bytes — not "publish returned nil".
	assert.Equal(t, strings.TrimRight(body, "\n"), f.remoteFile(t, "main", mybundleRemotePath),
		"the remote must hold the local file's bytes verbatim")

	// The reported SHA is a real commit on the remote's branch.
	assert.Equal(t, gitRun(t, f.bare, "rev-parse", "main"), result.SHA,
		"the reported commit must be the one the remote branch now points at")
	assert.True(t, result.Created, "the file did not exist at the remote before")
	assert.Equal(t, mybundleRemotePath, result.Path)

	// The commit carries the caller's subject.
	assert.Contains(t, gitRun(t, f.bare, "log", "-1", "--pretty=%s", "main"), "Add bundle mybundle")
}

func TestGitPublisher_PublishesTheSignatureSibling(t *testing.T) {
	body := "description: signed\n"
	f := newPublishFixture(t, "main", body)

	result, err := f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:    ItemTypeBundle,
		RemotePath:  mybundleRemotePath,
		Branch:      "main",
		SignPayload: func(payload []byte) ([]byte, error) { return []byte("ARMORED(" + string(payload) + ")"), nil },
	})
	require.NoError(t, err)
	require.True(t, result.Signed)

	assert.Equal(t, "ARMORED("+body+")", f.remoteFile(t, "main", mybundleRemotePath+".sig"),
		"the detached signature must land in the same branch as the content it covers")
	assert.Equal(t, strings.TrimRight(body, "\n"), f.remoteFile(t, "main", mybundleRemotePath))
}

func TestGitPublisher_SecondPublishUpdatesInPlace(t *testing.T) {
	f := newPublishFixture(t, "main", "description: v1\n")
	opts := PublishOptions{ItemType: ItemTypeBundle, RemotePath: mybundleRemotePath, Branch: "main"}

	first, err := f.pm.Publish(context.Background(), f.localPath, "shared", opts)
	require.NoError(t, err)
	require.True(t, first.Created)

	require.NoError(t, os.WriteFile(f.localPath, []byte("description: v2\n"), 0o644))
	second, err := f.pm.Publish(context.Background(), f.localPath, "shared", opts)
	require.NoError(t, err)

	assert.False(t, second.Created, "the second publish is an UPDATE; reporting 'Add' would misdescribe the change")
	assert.Equal(t, "description: v2", f.remoteFile(t, "main", mybundleRemotePath))
	assert.NotEqual(t, first.SHA, second.SHA, "a real second commit must have landed")
}

// An identical republish is a legitimate no-op — nothing to commit, and the
// remote already holds exactly what was asked for. It must not be an error
// (that would break every idempotent CI publish) and must not claim a new
// commit that does not exist.
func TestGitPublisher_IdenticalRepublishIsANoOpNotAnError(t *testing.T) {
	f := newPublishFixture(t, "main", "description: same\n")
	opts := PublishOptions{ItemType: ItemTypeBundle, RemotePath: mybundleRemotePath, Branch: "main"}

	first, err := f.pm.Publish(context.Background(), f.localPath, "shared", opts)
	require.NoError(t, err)

	second, err := f.pm.Publish(context.Background(), f.localPath, "shared", opts)
	require.NoError(t, err)

	assert.Equal(t, first.SHA, second.SHA, "no new commit exists, so none may be reported")
	assert.Equal(t, first.SHA, gitRun(t, f.bare, "rev-parse", "main"))
	assert.Equal(t, "description: same", f.remoteFile(t, "main", mybundleRemotePath))
}

// With no branch pinned, publish must land on the REMOTE's own default branch.
// The fixture's default is "trunk" precisely so a hardcoded "main" or "master"
// fails here.
func TestGitPublisher_UnpinnedBranchUsesTheRemotesDefault(t *testing.T) {
	f := newPublishFixture(t, "trunk", "description: on trunk\n")

	result, err := f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
	})
	require.NoError(t, err)

	assert.Equal(t, gitRun(t, f.bare, "rev-parse", "trunk"), result.SHA)
	assert.Equal(t, "description: on trunk", f.remoteFile(t, "trunk", mybundleRemotePath))
	gitFails(t, f.bare, "rev-parse", "--verify", "refs/heads/main")
}

// A generic git host has no pull-request API. The refusal must come BEFORE
// anything is written, so no orphan branch and no pushed content are left
// behind a PR that could never be opened.
func TestGitPublisher_PullRequestRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := newPublishFixture(t, "main", "description: pr\n")

	_, err := f.pm.Publish(context.Background(), f.localPath, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
		Branch:     "main",
		CreatePR:   true,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull request")

	// Nothing landed anywhere: one branch, one commit, no bundle.
	assert.Equal(t, "refs/heads/main", gitRun(t, f.bare, "for-each-ref", "--format=%(refname)", "refs/heads/"),
		"a refused PR publish must not create a branch")
	gitFails(t, f.bare, "show", "main:"+mybundleRemotePath)
}

func TestGitPublisher_UnreachableRemoteFailsLoudly(t *testing.T) {
	gitEnv(t)
	work := t.TempDir()
	local := filepath.Join(work, "mybundle.yaml")
	require.NoError(t, os.WriteFile(local, []byte("description: x\n"), 0o644))

	missing := "file://" + filepath.Join(work, "nope.git")
	fs := afero.NewOsFs()
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(fs))
	require.NoError(t, err)
	require.NoError(t, registry.Add("gone", missing))

	store := NewPublishRemoteStore(filepath.Join(work, "publish_remotes.yaml"), fs)
	_, serr := store.Set(NewPublishRemoteKey(missing), true)
	require.NoError(t, serr)

	pm := NewPublishManager(registry, AuthConfig{}, WithPublishFS(fs), WithPublishRemoteStore(store))
	_, err = pm.Publish(context.Background(), local, "gone", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: mybundleRemotePath,
	})
	require.Error(t, err, "an unreachable remote must fail, never report a publish that did not happen")
	assert.Contains(t, err.Error(), "clone")
}

func TestGitPublisher_RefusesEmptyContentAndEscapingPaths(t *testing.T) {
	p, err := NewGitPublisher("file:///srv/bundles.git")
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })
	ctx := context.Background()

	t.Run("zero bytes", func(t *testing.T) {
		// Refused before any clone is attempted, so a 0-byte write can never
		// replace real remote content with nothing.
		_, err := p.CreateOrUpdateFile(ctx, "", "", "a/b.yaml", "main", "msg", nil)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0 bytes")
	})

	for _, bad := range []string{"", "/etc/passwd", "../../escape.yaml", "a/../../b.yaml"} {
		t.Run(fmt.Sprintf("path %q", bad), func(t *testing.T) {
			_, err := p.CreateOrUpdateFile(ctx, "", "", bad, "main", "msg", []byte("x"))
			require.Error(t, err)
			_, err = p.GetFileSHA(ctx, "", "", bad, "main")
			require.Error(t, err)
		})
	}
}

// The whole decision behind this publisher is that ctxloom owns NO key
// material and NO host-key policy: the git binary resolves ~/.ssh/config,
// ssh-agent, credential helpers and known_hosts. A HostKeyCallback is the
// easy-to-get-quietly-wrong part, and getting it wrong is a silent MITM on the
// one path that pushes signed content.
//
// This is a STRUCTURAL pin rather than a behavioural one because the property
// belongs to the source, not to any output: ssh auth added tomorrow would pass
// every functional test in this package.
func TestGitPublisher_ContainsNoSSHOrHostKeyCode(t *testing.T) {
	// Resolved from this test's COMPILED-IN source path, never the working
	// directory: this package's TestMain chdirs into a temp dir, so a relative
	// read would find nothing — and a scan that finds nothing passes.
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "could not locate this test's own source file")
	body, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "git_publisher.go"))
	require.NoError(t, err)
	require.NotEmpty(t, body, "the scan read an empty file; it is not scanning what it thinks")

	// CODE only: the file's doc comment names ssh-agent and known_hosts on
	// purpose, to say who owns them. Scanning the comments would flag the very
	// prose that documents the decision.
	var code []string
	for _, line := range strings.Split(string(body), "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "//") {
			code = append(code, line)
		}
	}
	src := strings.Join(code, "\n")
	require.Contains(t, src, "func (p *GitPublisher) CreateOrUpdateFile",
		"the scan is not looking at the publisher it thinks it is")

	for _, forbidden := range []string{
		"HostKeyCallback", "knownhosts", "known_hosts",
		"golang.org/x/crypto/ssh", "ssh.PublicKeys", "ssh-agent", "SSH_AUTH_SOCK",
		"transport/ssh",
	} {
		assert.NotContains(t, src, forbidden,
			"%s appears in git_publisher.go's CODE: authentication and host-key policy belong to the user's git, "+
				"not to ctxloom — see the GitPublisher doc", forbidden)
	}
}
