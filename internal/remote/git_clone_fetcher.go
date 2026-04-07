package remote

import (
	"context"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
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
			return nil, fmt.Errorf("file not found: %s/%s/%s", owner, repo, filePath)
		}
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	reader, err := file.Reader()
	if err != nil {
		return nil, fmt.Errorf("failed to open file reader: %w", err)
	}
	defer reader.Close()

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
			return nil, fmt.Errorf("directory not found: %s/%s/%s", owner, repo, dirPath)
		}
	}

	var entries []DirEntry
	for _, entry := range tree.Entries {
		entries = append(entries, DirEntry{
			Name:  entry.Name,
			IsDir: entry.Mode.IsFile() == false,
			SHA:   entry.Hash.String(),
		})
	}

	return entries, nil
}

// ResolveRef converts a git reference to a commit SHA.
func (f *GitCloneFetcher) ResolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	// Try as a full commit SHA or abbreviated SHA
	if len(ref) >= 7 && len(ref) <= 40 {
		hash, err := f.repo.ResolveRevision(plumbing.Revision(ref))
		if err == nil {
			return hash.String(), nil
		}
	}

	// Try as origin/branch
	hash, err := f.repo.ResolveRevision(plumbing.Revision("refs/remotes/origin/" + ref))
	if err == nil {
		return hash.String(), nil
	}

	// Try as a tag
	hash, err = f.repo.ResolveRevision(plumbing.Revision("refs/tags/" + ref))
	if err == nil {
		return hash.String(), nil
	}

	// Try as a direct revision (go-git handles various formats)
	hash, err = f.repo.ResolveRevision(plumbing.Revision(ref))
	if err == nil {
		return hash.String(), nil
	}

	return "", fmt.Errorf("ref not found: %s", ref)
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

	_, err = tree.Tree("ctxloom/v1")
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
		// Use HEAD
		head, err := f.repo.Head()
		if err != nil {
			return nil, fmt.Errorf("failed to get HEAD: %w", err)
		}
		commitHash = head.Hash()
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
	// Try direct resolution strategies in order
	strategies := []string{
		ref,
		"refs/remotes/origin/" + ref,
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

	return plumbing.ZeroHash, fmt.Errorf("ref not found: %s", ref)
}

// pathInTree checks if a path exists in a tree. Helper for directory checks.
func pathInTree(tree *object.Tree, p string) bool {
	parts := strings.Split(path.Clean(p), "/")
	current := tree
	for i, part := range parts {
		entry, err := current.FindEntry(part)
		if err != nil {
			return false
		}
		if i < len(parts)-1 {
			// Need to traverse into subdirectory
			subtree, err := current.Tree(part)
			if err != nil {
				return false
			}
			current = subtree
		} else {
			_ = entry // found the final component
		}
	}
	return true
}
