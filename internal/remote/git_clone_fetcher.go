package remote

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// GitCloneFetcher implements Fetcher by reading from a local git clone.
// All operations are local filesystem reads — zero network calls.
type GitCloneFetcher struct {
	repoDir   string
	repoURL   string
	repo      *git.Repository
	forgeType ForgeType
	auth      transport.AuthMethod
}

// NewGitCloneFetcher creates a fetcher backed by a local git clone.
func NewGitCloneFetcher(repoDir, repoURL string, forgeType ForgeType, auth transport.AuthMethod) (*GitCloneFetcher, error) {
	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to open cached repo: %w", err)
	}
	return &GitCloneFetcher{
		repoDir:   repoDir,
		repoURL:   repoURL,
		repo:      repo,
		forgeType: forgeType,
		auth:      auth,
	}, nil
}

// Forge returns the forge type.
func (f *GitCloneFetcher) Forge() ForgeType {
	return f.forgeType
}

// FetchFile retrieves raw file content from the local clone.
func (f *GitCloneFetcher) FetchFile(ctx context.Context, owner, repo, filePath, ref string) ([]byte, error) {
	tree, err := f.treeAtRef(ref)
	if err != nil {
		return nil, err
	}

	file, err := tree.File(filePath)
	if err != nil {
		if err == object.ErrFileNotFound {
			return nil, fmt.Errorf("file not found: %s/%s/%s: %w", owner, repo, filePath, errs.ErrRemoteContentNotFound)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to open file reader: %w", err)
	}
	defer func() { _ = reader.Close() }()

	content, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read file content: %w", err)
	}

	return content, nil
}

// ListDir lists directory contents at the specified path.
func (f *GitCloneFetcher) ListDir(ctx context.Context, owner, repo, dirPath, ref string) ([]DirEntry, error) {
	tree, err := f.treeAtRef(ref)
	if err != nil {
		return nil, err
	}

	// Navigate to the subdirectory
	if dirPath != "" && dirPath != "." {
		tree, err = tree.Tree(dirPath)
		if err != nil {
			return nil, fmt.Errorf("directory not found: %s/%s/%s: %w", owner, repo, dirPath, errs.ErrRemoteContentNotFound)
		}
	}

	var entries []DirEntry
	for _, entry := range tree.Entries {
		entries = append(entries, DirEntry{
			Name:  entry.Name,
			IsDir: !entry.Mode.IsFile(),
			SHA:   entry.Hash.String(),
		})
	}

	return entries, nil
}

// ListDeletedItems walks the repo's commit history and returns the item paths
// under ctxloom/<kind>/ that existed at some past revision but are ABSENT at
// HEAD — items removed upstream. Paths are relative to ctxloom/<kind>/ with the
// .yaml suffix stripped, matching ListDir-derived current listings. This is the
// history capability behind VCS Versioned.ListDeletedItems; it reads the local
// clone only (zero network). Repos with no history of the kind list nothing.
func (f *GitCloneFetcher) ListDeletedItems(ctx context.Context, kind ItemType) ([]string, error) {
	base := "ctxloom/" + kind.DirName()

	// Items present at HEAD — the baseline we subtract from history.
	present := map[string]struct{}{}
	if head, err := f.treeAtRef(""); err == nil {
		collectItemPaths(head, base, present)
	}

	// Union of every item path ever seen under base across all commits.
	everSeen := map[string]struct{}{}
	iter, err := f.repo.Log(&git.LogOptions{})
	if err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	defer iter.Close()
	if err := iter.ForEach(func(c *object.Commit) error {
		tree, terr := c.Tree()
		if terr != nil {
			return nil // skip a commit whose tree won't load
		}
		collectItemPaths(tree, base, everSeen)
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk history: %w", err)
	}

	var deleted []string
	for p := range everSeen {
		if _, stillPresent := present[p]; !stillPresent {
			deleted = append(deleted, p)
		}
	}
	sort.Strings(deleted)
	return deleted, nil
}

// collectItemPaths adds every .yaml item path under base in tree to out, keyed
// relative to base with the suffix stripped (e.g. "lang/go/testing"). A tree
// without base contributes nothing.
func collectItemPaths(tree *object.Tree, base string, out map[string]struct{}) {
	sub, err := tree.Tree(base)
	if err != nil {
		return
	}
	files := sub.Files()
	defer files.Close()
	_ = files.ForEach(func(file *object.File) error {
		if strings.HasSuffix(file.Name, ".yaml") {
			out[strings.TrimSuffix(file.Name, ".yaml")] = struct{}{}
		}
		return nil
	})
}

// ResolveRef converts a git reference to a commit SHA. Resolution order is
// load-bearing: refs/remotes/origin/<ref> is tried BEFORE the bare revision.
// git fetch advances the remote-tracking refs but never the clone's local
// branches (refs/heads/*), and go-git's bare ResolveRevision walks the rev-parse
// rules — including refs/heads/<ref> — so resolving a branch name bare returns
// the stale clone-time commit. Trying origin/ first makes a branch name (e.g. a
// default branch named "develop") resolve to the freshly-fetched tip; a SHA or
// tag falls through to the bare resolution. This mirrors resolveToCommitHash.
func (f *GitCloneFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	for _, rev := range []string{
		"refs/remotes/origin/" + ref,
		"refs/tags/" + ref,
		ref,
	} {
		if hash, err := f.repo.ResolveRevision(plumbing.Revision(rev)); err == nil {
			return hash.String(), nil
		}
	}

	return "", fmt.Errorf("ref not found: %s: %w", ref, errs.ErrRemoteContentNotFound)
}

// ListTags returns the repository's tag names (e.g. "v1.2.3"), lightweight and
// annotated alike, in no particular order. It reads refs/tags from the local
// clone — zero network. This is the version space the semver constraint resolver
// matches against.
func (f *GitCloneFetcher) ListTags(ctx context.Context, owner, repo string) ([]string, error) {
	iter, err := f.repo.Tags()
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer iter.Close()
	var tags []string
	if err := iter.ForEach(func(ref *plumbing.Reference) error {
		tags = append(tags, ref.Name().Short())
		return nil
	}); err != nil {
		return nil, fmt.Errorf("iterate tags: %w", err)
	}
	return tags, nil
}

// SearchRepos is not supported by the local clone fetcher.
// This should be handled by the API fallback wrapper.
func (f *GitCloneFetcher) SearchRepos(ctx context.Context, query string, limit int) ([]RepoInfo, error) {
	return nil, fmt.Errorf("SearchRepos is not supported by the git clone fetcher")
}

// ValidateRepo checks if the repository has valid ctxloom structure.
func (f *GitCloneFetcher) ValidateRepo(ctx context.Context, owner, repo string) (bool, error) {
	head, err := f.repo.Head()
	if err != nil {
		return false, nil
	}

	commit, err := f.repo.CommitObject(head.Hash())
	if err != nil {
		return false, nil
	}

	tree, err := commit.Tree()
	if err != nil {
		return false, nil
	}

	_, err = tree.Tree("ctxloom")
	return err == nil, nil
}

// GetDefaultBranch returns the default branch name.
// Uses only local data — no network calls. For shallow clones, HEAD
// points to the default branch that was cloned.
func (f *GitCloneFetcher) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	// Try HEAD directly — for non-bare clones (including shallow), HEAD
	// points to the default branch
	head, err := f.repo.Head()
	if err == nil && head.Name().IsBranch() {
		return head.Name().Short(), nil
	}

	// Try to read the origin HEAD symref
	ref, err := f.repo.Reference(plumbing.NewRemoteReferenceName("origin", "HEAD"), true)
	if err == nil {
		name := ref.Target().Short()
		name = strings.TrimPrefix(name, "origin/")
		if name != "" {
			return name, nil
		}
	}

	// Look for common default branch names in remote refs
	for _, name := range []string{"main", "master"} {
		_, err := f.repo.Reference(plumbing.NewRemoteReferenceName("origin", name), false)
		if err == nil {
			return name, nil
		}
	}

	return "main", nil
}

// treeAtRef resolves a ref and returns the commit tree.
func (f *GitCloneFetcher) treeAtRef(ref string) (*object.Tree, error) {
	var commitHash plumbing.Hash

	if ref == "" {
		// An empty ref means "the remote's default-branch tip" (the VCS
		// contract), NOT the clone's local HEAD: fetch advances
		// refs/remotes/origin/* but never fast-forwards the local branch, so
		// HEAD serves clone-time content after every sync. Resolve through the
		// default branch name (resolveToCommitHash prefers origin/<name>),
		// falling back to HEAD only when no branch resolves (fixture repos
		// without origin refs).
		if name, derr := f.GetDefaultBranch(context.Background(), "", ""); derr == nil && name != "" {
			if hash, rerr := f.repo.ResolveRevision(plumbing.Revision("refs/remotes/origin/" + name)); rerr == nil {
				commitHash = *hash
			}
		}
		if commitHash.IsZero() {
			head, err := f.repo.Head()
			if err != nil {
				return nil, fmt.Errorf("failed to get HEAD: %w", err)
			}
			commitHash = head.Hash()
		}
	} else {
		// Try resolving the ref through multiple strategies
		resolved, err := f.resolveToCommitHash(ref)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve ref '%s': %w", ref, err)
		}
		commitHash = resolved
	}

	commit, err := f.repo.CommitObject(commitHash)
	if err != nil {
		return nil, fmt.Errorf("failed to get commit: %w", err)
	}

	tree, err := commit.Tree()
	if err != nil {
		return nil, fmt.Errorf("failed to get tree: %w", err)
	}

	return tree, nil
}

// resolveToCommitHash tries multiple strategies to resolve a ref string to a commit hash.
func (f *GitCloneFetcher) resolveToCommitHash(ref string) (plumbing.Hash, error) {
	// origin/<ref> first: fetch advances the remote-tracking refs but never
	// the clone's local branches, so a local branch name resolves to stale
	// clone-time state. SHAs and tags fall through to the later strategies.
	strategies := []string{
		"refs/remotes/origin/" + ref,
		ref,
		"refs/tags/" + ref,
	}

	for _, s := range strategies {
		hash, err := f.repo.ResolveRevision(plumbing.Revision(s))
		if err == nil {
			return *hash, nil
		}
	}

	// Try looking up annotated tags
	tagRef, err := f.repo.Tag(ref)
	if err == nil {
		tagObj, err := f.repo.TagObject(tagRef.Hash())
		if err == nil {
			commit, err := tagObj.Commit()
			if err == nil {
				return commit.Hash, nil
			}
		}
		// Lightweight tag — hash is the commit directly
		return tagRef.Hash(), nil
	}

	return plumbing.ZeroHash, fmt.Errorf("ref not found: %s: %w", ref, errs.ErrRemoteContentNotFound)
}
