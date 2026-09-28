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
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/tasks/taskstest"
	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
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
	taskstest.Git(t, root, nil, "init", "--bare", "-b", branch, bare)

	seed := filepath.Join(root, "seed")
	require.NoError(t, os.MkdirAll(seed, 0o755))
	taskstest.Git(t, seed, nil, "init", "-b", branch)
	require.NoError(t, os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644))
	taskstest.Git(t, seed, nil, "add", "-A")
	taskstest.Git(t, seed, nil, "commit", "-m", "seed")
	taskstest.Git(t, seed, nil, "remote", "add", "origin", bare)
	taskstest.Git(t, seed, nil, "push", "origin", branch)

	return "file://" + bare, bare
}

// publishFixture wires a PublishManager over a real local file and a real
// file:// remote. Registering the remote is the whole admission story —
// publish_registration_consent_test.go is where that claim is asserted
// directly.
type publishFixture struct {
	pm        *PublishManager
	remoteURL string
	bare      string
}

func newPublishFixture(t *testing.T, branch string) *publishFixture {
	t.Helper()
	gitEnv(t)
	url, bare := bareRemote(t, branch)

	work := t.TempDir()
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(afero.NewOsFs()))
	require.NoError(t, err)
	require.NoError(t, registry.Add("shared", url))

	return &publishFixture{pm: NewPublishManager(registry, AuthConfig{}), remoteURL: url, bare: bare}
}

// publishEnvelope publishes a one-file tree — the bundle's envelope, body —
// at mybundleRemotePath.
func (f *publishFixture) publishEnvelope(t *testing.T, body string, opts PublishOptions) (*PublishResult, error) {
	t.Helper()
	opts.ItemType = ItemTypeBundle
	opts.RemotePath = mybundleRemotePath
	return f.pm.PublishTree(context.Background(), map[string][]byte{"bundle.yaml": []byte(body)}, "shared", opts)
}

// mybundleEnvelope is where publishEnvelope's file lands in the remote.
const mybundleEnvelope = mybundleRemotePath + "/bundle.yaml"

// remoteFile reads a path out of the BARE repository at branch — the
// destination's own view, not the publisher's.
func (f *publishFixture) remoteFile(t *testing.T, branch, path string) string {
	t.Helper()
	return taskstest.Git(t, f.bare, nil, "show", branch+":"+path)
}

func TestGitPublisher_PublishLandsInTheBareRepository(t *testing.T) {
	body := "description: Test bundle\n"
	f := newPublishFixture(t, "main")

	result, err := f.publishEnvelope(t, body, PublishOptions{Branch: "main", Title: "Add bundle mybundle"})
	require.NoError(t, err)

	// The BARE REPO holds the exact local bytes — not "publish returned nil".
	assert.Equal(t, strings.TrimRight(body, "\n"), f.remoteFile(t, "main", mybundleEnvelope),
		"the remote must hold the local file's bytes verbatim")

	// The reported SHA is a real commit on the remote's branch.
	assert.Equal(t, taskstest.Git(t, f.bare, nil, "rev-parse", "main"), result.SHA,
		"the reported commit must be the one the remote branch now points at")
	assert.Equal(t, mybundleRemotePath, result.Path)

	// The commit carries the caller's subject.
	assert.Contains(t, taskstest.Git(t, f.bare, nil, "log", "-1", "--pretty=%s", "main"), "Add bundle mybundle")
}

// TestGitPublisher_PublishTreeLandsAsOneCommit is
// PublishLandsInTheBareRepository's whole-tree counterpart, and the real-git
// proof for engaged-chivalry: every file in the batch lands in the bare
// repo, byte for byte, and the whole batch is EXACTLY ONE new commit on the
// branch — not one per file. That is the entire reason
// Publisher.CreateOrUpdateFiles exists: a tree published file by file can
// fail part way, leaving a bundle whose SHA256SUMS covers files that never
// arrived, and one commit makes that impossible rather than merely unlikely.
func TestGitPublisher_PublishTreeLandsAsOneCommit(t *testing.T) {
	f := newPublishFixture(t, "main")
	beforeCount := taskstest.Git(t, f.bare, nil, "rev-list", "--count", "main")

	const root = ".ctxloom/content/bundles/v1/atelier"
	files := map[string][]byte{
		"bundle.yaml":           []byte("version: 1.0.0\nskills:\n  greet: {}\n"),
		"skills/greet/SKILL.md": []byte("# greet\n\nSay hello.\n"),
	}
	result, err := f.pm.PublishTree(context.Background(), files, "shared", PublishOptions{
		ItemType:   ItemTypeBundle,
		RemotePath: root,
		Branch:     "main",
		Title:      "Add bundle atelier",
	})
	require.NoError(t, err)

	// The BARE REPO holds every file's exact bytes — not "PublishTree
	// returned nil".
	assert.Equal(t, "version: 1.0.0\nskills:\n  greet: {}",
		f.remoteFile(t, "main", root+"/bundle.yaml"))
	assert.Equal(t, "# greet\n\nSay hello.",
		f.remoteFile(t, "main", root+"/skills/greet/SKILL.md"))

	afterCount := taskstest.Git(t, f.bare, nil, "rev-list", "--count", "main")
	before, err1 := strconv.Atoi(beforeCount)
	after, err2 := strconv.Atoi(afterCount)
	require.NoError(t, err1)
	require.NoError(t, err2)
	assert.Equal(t, before+1, after,
		"the whole tree must land as exactly ONE new commit, not one per file")

	assert.Equal(t, taskstest.Git(t, f.bare, nil, "rev-parse", "main"), result.SHA,
		"the reported commit is the one the remote branch now points at")
	assert.Equal(t, root, result.Path)
}

func TestGitPublisher_SecondPublishUpdatesInPlace(t *testing.T) {
	f := newPublishFixture(t, "main")
	opts := PublishOptions{Branch: "main"}

	first, err := f.publishEnvelope(t, "description: v1\n", opts)
	require.NoError(t, err)

	second, err := f.publishEnvelope(t, "description: v2\n", opts)
	require.NoError(t, err)

	assert.Equal(t, "description: v2", f.remoteFile(t, "main", mybundleEnvelope))
	assert.NotEqual(t, first.SHA, second.SHA, "a real second commit must have landed")
}

// An identical republish is a legitimate no-op — nothing to commit, and the
// remote already holds exactly what was asked for. It must not be an error
// (that would break every idempotent CI publish) and must not claim a new
// commit that does not exist.
func TestGitPublisher_IdenticalRepublishIsANoOpNotAnError(t *testing.T) {
	f := newPublishFixture(t, "main")
	opts := PublishOptions{Branch: "main"}

	first, err := f.publishEnvelope(t, "description: same\n", opts)
	require.NoError(t, err)

	second, err := f.publishEnvelope(t, "description: same\n", opts)
	require.NoError(t, err)

	assert.Equal(t, first.SHA, second.SHA, "no new commit exists, so none may be reported")
	assert.Equal(t, first.SHA, taskstest.Git(t, f.bare, nil, "rev-parse", "main"))
	assert.Equal(t, "description: same", f.remoteFile(t, "main", mybundleEnvelope))
}

// With no branch pinned, publish must land on the REMOTE's own default branch.
// The fixture's default is "trunk" precisely so a hardcoded "main" or "master"
// fails here.
func TestGitPublisher_UnpinnedBranchUsesTheRemotesDefault(t *testing.T) {
	f := newPublishFixture(t, "trunk")

	result, err := f.publishEnvelope(t, "description: on trunk\n", PublishOptions{})
	require.NoError(t, err)

	assert.Equal(t, taskstest.Git(t, f.bare, nil, "rev-parse", "trunk"), result.SHA)
	assert.Equal(t, "description: on trunk", f.remoteFile(t, "trunk", mybundleEnvelope))
	gitFails(t, f.bare, "rev-parse", "--verify", "refs/heads/main")
}

// A generic git host has no pull-request API. The refusal must come BEFORE
// anything is written, so no orphan branch and no pushed content are left
// behind a PR that could never be opened.
func TestGitPublisher_PullRequestRefusedBeforeAnythingIsWritten(t *testing.T) {
	f := newPublishFixture(t, "main")

	_, err := f.publishEnvelope(t, "description: pr\n", PublishOptions{Branch: "main", CreatePR: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pull request")

	// Nothing landed anywhere: one branch, one commit, no bundle.
	assert.Equal(t, "refs/heads/main", taskstest.Git(t, f.bare, nil, "for-each-ref", "--format=%(refname)", "refs/heads/"),
		"a refused PR publish must not create a branch")
	gitFails(t, f.bare, "show", "main:"+mybundleEnvelope)
}

func TestGitPublisher_UnreachableRemoteFailsLoudly(t *testing.T) {
	gitEnv(t)
	work := t.TempDir()
	missing := "file://" + filepath.Join(work, "nope.git")
	registry, err := NewRegistry(filepath.Join(work, "remotes.yaml"), WithRegistryFS(afero.NewOsFs()))
	require.NoError(t, err)
	require.NoError(t, registry.Add("gone", missing))

	pm := NewPublishManager(registry, AuthConfig{})
	_, err = pm.PublishTree(context.Background(), map[string][]byte{"bundle.yaml": []byte("description: x\n")}, "gone", PublishOptions{
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
		_, err := p.CreateOrUpdateFiles(ctx, "", "", "main", "msg", map[string][]byte{"a/b.yaml": nil})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "0 bytes")
	})

	for _, bad := range []string{"", "/etc/passwd", "../../escape.yaml", "a/../../b.yaml"} {
		t.Run(fmt.Sprintf("path %q", bad), func(t *testing.T) {
			_, err := p.CreateOrUpdateFiles(ctx, "", "", "main", "msg", map[string][]byte{bad: []byte("x")})
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
	dir, err := sourcedir.Dir()
	require.NoError(t, err, "could not locate this package's source directory")
	body, err := os.ReadFile(filepath.Join(dir, "git_publisher.go"))
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
