package remote

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	igit "github.com/ctxloom/ctxloom/internal/git"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// GitPublisher publishes to ANY git remote — file://, ssh://, git://, a
// self-hosted https forge — by doing what a person would do: clone it, write
// the file, commit, push. It is the non-GitHub half of NewPublisher; github.com
// keeps its forge-API path unchanged.
//
// THE USER'S GIT OWNS AUTHENTICATION. Every operation runs through
// internal/git, which shells out to the git binary, which resolves
// ~/.ssh/config, ssh-agent, credential helpers and known_hosts exactly as it
// does for every other repository on the machine. ctxloom holds no key
// material, discovers no keys, negotiates with no agent and installs no
// host-key policy — there is deliberately not one line of ssh code here.
// A HostKeyCallback is the easy-to-get-quietly-wrong part of speaking ssh, and
// getting it wrong means a silent MITM on the one path that pushes SIGNED
// content. The credentials the user has already configured, and already trusts
// for every other repo, are the ones that publish.
//
// The accepted cost is that generic publish needs a git binary on PATH, and
// that two transports now coexist in this package. The rejected alternative
// was native go-git ssh auth, which would have made ctxloom the owner of key
// discovery, agent negotiation and host-key policy.
//
// ONE INSTANCE, ONE CLONE, ONE BRANCH. The clone is made lazily on first use
// and reused for every subsequent call, so a publish that writes a bundle and
// its detached signature clones once and pushes twice. Call Close to remove
// the working clone.
type GitPublisher struct {
	// repoURL is the transport spelling handed to git (RepoURL.CloneArg).
	repoURL string
	git     igit.Git

	// dir is the working clone; "" until the first operation creates it.
	// branch is what that clone has checked out.
	dir    string
	branch string
}

// gitPublisherRemote is the name git gives the clone's origin. Pinned as a
// constant because the push refspec and the landed-ref check must name the
// same remote — they are one fact, not two.
const gitPublisherRemote = "origin"

// NewGitPublisher builds a publisher for any git-reachable repository.
// repoURL is normalised to its TRANSPORT spelling (RepoURL.CloneArg), which is
// what preserves an scp-style "git@host:owner/repo" instead of rewriting it to
// https and discarding the user's chosen transport — and their ssh key with it.
func NewGitPublisher(repoURL string) (*GitPublisher, error) {
	parsed, err := ParseRepoURL(repoURL)
	if err != nil {
		return nil, fmt.Errorf("publish to %q: %w", repoURL, err)
	}
	if parsed.Kind() != SourceKindRemote {
		return nil, fmt.Errorf("publish to %q: %s is not a git remote", repoURL, parsed.Kind())
	}
	return &GitPublisher{repoURL: parsed.CloneArg(), git: igit.NewExec()}, nil
}

// Close removes the working clone. A publisher whose clone was never made (an
// error before the first operation) closes cleanly.
func (p *GitPublisher) Close() error {
	if p == nil || p.dir == "" {
		return nil
	}
	dir := p.dir
	p.dir, p.branch = "", ""
	return os.RemoveAll(dir)
}

// workTree returns the working clone, making it on first use.
//
// An empty branch clones the remote's own default and reports it — that is how
// defaultBranch answers without guessing "main" or "master". A non-empty
// branch is checked out by the clone itself, so a branch that does not exist
// at the remote fails HERE, loudly, before anything is written.
//
// A second, different branch is refused rather than silently re-cloned: this
// type is one clone of one branch, and the two callers that reach it within a
// publish (the existing-file probe and the two writes) always name the same
// one. Silently serving a different branch than the one asked for would
// publish somewhere the caller never named.
func (p *GitPublisher) workTree(ctx context.Context, branch string) (string, error) {
	if p.dir != "" {
		if branch != "" && branch != p.branch {
			return "", fmt.Errorf("publish to %s: this publisher is on branch %q and was asked for %q; one publisher clones one branch", p.repoURL, p.branch, branch)
		}
		return p.dir, nil
	}

	dir, err := os.MkdirTemp("", "ctxloom-publish-")
	if err != nil {
		return "", fmt.Errorf("publish to %s: create a working clone directory: %w", p.repoURL, err)
	}
	if err := p.git.Clone(ctx, p.repoURL, dir, branch); err != nil {
		_ = os.RemoveAll(dir)
		return "", fmt.Errorf("publish to %s: clone: %w", p.repoURL, err)
	}

	checked := branch
	if checked == "" {
		// Read back what the remote's HEAD actually put us on.
		checked, err = p.git.CurrentBranch(ctx, dir)
		if err != nil {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("publish to %s: read the cloned branch: %w", p.repoURL, err)
		}
		if checked == "" || checked == detachedHeadSentinel {
			_ = os.RemoveAll(dir)
			return "", fmt.Errorf("publish to %s: the clone is not on a branch (%q), so there is no default branch to publish to", p.repoURL, checked)
		}
	}
	p.dir, p.branch = dir, checked
	return p.dir, nil
}

// detachedHeadSentinel is git's own name for "HEAD is not on a branch"
// (rev-parse --abbrev-ref HEAD). See igit.Git.CurrentBranch.
const detachedHeadSentinel = "HEAD"

// defaultBranch reports the remote's default branch, by cloning it and reading
// what git checked out. It satisfies the unexported capability
// resolvePublishBranch prefers over the fetcher: the generic git adapter has no
// API fetcher at all, and this publisher is about to clone the repository
// anyway, so asking it costs nothing extra.
func (p *GitPublisher) defaultBranch(ctx context.Context) (string, error) {
	if _, err := p.workTree(ctx, ""); err != nil {
		return "", err
	}
	return p.branch, nil
}

// GetFileSHA reports the blob SHA of path at ref, or "" when the ref's tree has
// no such path. owner/repo are ignored: a git URL is a whole clone argument,
// not an owner/repo pair (see the Publisher interface — those two segments are
// a forge-API path shape).
//
// An empty ref reads the checked-out branch.
func (p *GitPublisher) GetFileSHA(ctx context.Context, _, _, filePath, ref string) (string, error) {
	if err := checkRepoRelPath(filePath); err != nil {
		return "", err
	}
	dir, err := p.workTree(ctx, ref)
	if err != nil {
		return "", err
	}
	at := ref
	if at == "" {
		at = p.branch
	}
	return p.git.FileBlobSHA(ctx, dir, at, filePath)
}

// CreateOrUpdateFile writes content at filePath on branch and pushes it,
// returning the commit SHA that landed.
//
// IT REPORTS SUCCESS ONLY WHEN THE REMOTE ACTUALLY MOVED. Every step is
// checked against repository state rather than against a nil error: the commit
// must carry a diff that includes filePath, and the remote's branch ref must
// afterwards point at the new commit. A `git push` that exits 0 and a remote
// holding the commit are two different facts, and reporting a publish that
// wrote nothing is this codebase's characteristic bug.
//
// An UNCHANGED republish is a legitimate no-op, not a failure: when the file
// already holds exactly these bytes there is nothing to commit, so the current
// commit is returned after confirming the remote still points at it. The
// destination ends up in precisely the state the caller asked for, which is
// the outcome being reported.
func (p *GitPublisher) CreateOrUpdateFile(ctx context.Context, _, _, filePath, branch, message string, content []byte) (string, error) {
	if len(content) == 0 {
		return "", fmt.Errorf("refusing to publish 0 bytes to %s: an empty write would replace the remote's content with nothing", filePath)
	}
	if err := checkRepoRelPath(filePath); err != nil {
		return "", err
	}
	dir, err := p.workTree(ctx, branch)
	if err != nil {
		return "", err
	}
	target := p.branch

	full := filepath.Join(dir, filepath.FromSlash(filePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("publish %s: create its directory in the working clone: %w", filePath, err)
	}
	// No AllowEmpty: content's zero-length case is already refused above, so
	// the default empty-over-existing guard can never fire on a legitimate
	// call here.
	if err := iox.WriteFileAtomic(full, content, 0o644); err != nil {
		return "", fmt.Errorf("publish %s: write it into the working clone: %w", filePath, err)
	}

	dirty, err := p.git.IsDirty(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("publish %s: check the working clone for changes: %w", filePath, err)
	}
	if !dirty {
		return p.unchanged(ctx, dir, filePath, target)
	}

	sha, changed, err := p.git.CommitAll(ctx, dir, message)
	if err != nil {
		return "", fmt.Errorf("publish %s: commit: %w", filePath, err)
	}
	if !slices.Contains(changed, filePath) {
		return "", fmt.Errorf("publish %s: commit %s landed but does not contain it (it changed %v); nothing was pushed", filePath, sha, changed)
	}
	if err := p.git.Push(ctx, dir, gitPublisherRemote, "HEAD:"+branchRef(target)); err != nil {
		return "", fmt.Errorf("publish %s: push to %s: %w", filePath, p.repoURL, err)
	}
	if err := p.confirmLanded(ctx, dir, target, sha); err != nil {
		return "", fmt.Errorf("publish %s: %w", filePath, err)
	}
	return sha, nil
}

// unchanged handles the republish-of-identical-bytes case: no commit, no push,
// but still a confirmation that the remote holds what this clone holds. Without
// that check a remote that moved under us since the clone would be reported as
// carrying content it does not have.
func (p *GitPublisher) unchanged(ctx context.Context, dir, filePath, branch string) (string, error) {
	sha, err := p.git.HeadSHA(ctx, dir)
	if err != nil {
		return "", fmt.Errorf("publish %s: it is already up to date, but the current commit could not be read: %w", filePath, err)
	}
	if err := p.confirmLanded(ctx, dir, branch, sha); err != nil {
		return "", fmt.Errorf("publish %s: it is unchanged here, but %w", filePath, err)
	}
	return sha, nil
}

// confirmLanded asserts the REMOTE's branch ref points at want. This is the
// only evidence that a push did anything; a nil error from `git push` is not.
func (p *GitPublisher) confirmLanded(ctx context.Context, dir, branch, want string) error {
	landed, err := p.git.RemoteRefSHA(ctx, dir, gitPublisherRemote, branchRef(branch))
	if err != nil {
		return fmt.Errorf("could not verify what %s now holds on %s: %w", p.repoURL, branch, err)
	}
	if landed == "" {
		return fmt.Errorf("%s reports no branch %s after the push, so nothing landed", p.repoURL, branch)
	}
	if landed != want {
		return fmt.Errorf("%s has %s at %s, not the expected %s (it moved under this publish)", p.repoURL, branch, landed, want)
	}
	return nil
}

// branchRef renders a branch as its fully-qualified ref. Fully qualified on
// purpose: an unqualified refspec lets git resolve the destination against the
// remote's own rules, which can create a ref somewhere other than
// refs/heads when a tag or another namespace shares the name.
func branchRef(branch string) string {
	return "refs/heads/" + branch
}

// pullRequestSupport declares up front that this publisher cannot open pull
// requests, so the PR strategy is refused BEFORE any branch, commit or push —
// no orphan branch and no pushed content behind a PR that could never be
// opened. See the unexported pullRequestRefuser capability in publish.go.
func (p *GitPublisher) pullRequestSupport() error {
	return errNoPullRequests(p.repoURL)
}

// CreateBranch is REFUSED for the same reason as CreatePullRequest: a branch
// pushed for a PR that cannot be opened is litter on someone else's
// repository. Reachable only if a caller drives the Publisher interface
// directly, past pullRequestSupport.
func (p *GitPublisher) CreateBranch(_ context.Context, _, _, _, _ string) error {
	return errNoPullRequests(p.repoURL)
}

// CreatePullRequest is REFUSED: see CreateBranch. It is kept honest rather
// than silently downgraded to a direct push — "I opened a PR" and "I pushed to
// your default branch" are not interchangeable outcomes.
func (p *GitPublisher) CreatePullRequest(_ context.Context, _, _, _, _, _, _ string) (string, error) {
	return "", errNoPullRequests(p.repoURL)
}

// errNoPullRequests is the one wording for the PR refusal, shared by both
// methods so they cannot drift into two different explanations of one fact.
func errNoPullRequests(repoURL string) error {
	return fmt.Errorf("cannot open a pull request on %s: pull requests are a forge API and %s is reached as plain git, which has none. Publish directly instead (drop --pr)", repoURL, repoURL)
}

// checkRepoRelPath refuses a repo path that would escape the working clone.
// The path is caller-computed (PublishOptions.RemotePath) rather than
// user-typed, but it is written to the filesystem here, and "the caller is
// careful" is not a containment guarantee.
func checkRepoRelPath(p string) error {
	if strings.TrimSpace(p) == "" {
		return fmt.Errorf("refusing to publish to an empty path")
	}
	if path.IsAbs(p) || filepath.IsAbs(p) || strings.HasPrefix(p, "/") {
		return fmt.Errorf("refusing to publish to %q: a repository path must be relative to the repository root", p)
	}
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return fmt.Errorf("refusing to publish to %q: it climbs out of the repository", p)
		}
	}
	return nil
}
