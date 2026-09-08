package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// ErrNoCloneForWorktree reports that the repository a worktree was asked for
// has no local clone. A worktree is a checkout OF a clone, so this is the one
// precondition the caller has to satisfy first.
var ErrNoCloneForWorktree = errors.New("no local clone of the repository to take a worktree from")

// EnsureSparseWorktree checks the tree at subpath, at commit sha, out into
// worktreeDir as a git worktree of repoURL's clone, and returns the directory
// subpath landed in.
//
// WHY A WORKTREE AND NOT A COPY. A copied materialization is a second piece of
// state describing the same pin, and nothing keeps the two in step: advancing a
// pin and refreshing the copy are separate actions, so every ordering that ran
// them apart — a pin moved with no copy refresh, a copy refreshed past a held
// pin — produced a cache that disagreed with the lockfile while looking
// perfectly well-formed. Git owns a worktree's contents, so the pin and the
// bytes are one fact: `checkout --detach <sha>` cannot half-succeed into a tree
// that claims a commit it does not hold.
//
// ONE WORKTREE PER PINNED BUNDLE, not per repository. Two bundles published by
// one repository may be pinned at different commits, and a single shared
// checkout can only be at one commit — so the worktree, not the clone, is what
// carries the pin. They all share the clone's object store, so the cost of the
// second worktree is its checked-out files and nothing else.
//
// THE CHECKOUT IS NARROWED to subpath with a sparse-checkout in --no-cone mode,
// which takes the literal path rather than a cone pattern. The worktree is
// added --no-checkout first so the full repository is never written to disk and
// then trimmed; only subpath is ever materialized.
//
// FORCED, because this tree is DERIVED. A local edit inside the cache must not
// be able to make the checkout refuse and strand the worktree off its pin —
// re-ensuring is precisely how a caller says "make this match the pin again".
//
// The bytes are laid out at subpath's REPOSITORY path inside worktreeDir, which
// is why the returned directory is nested rather than being worktreeDir itself.
// Reference.LocalWorktreePath and Reference.LocalTreePath name the same two
// directories from the reading side.
func (c *RepoCache) EnsureSparseWorktree(ctx context.Context, repoURL, sha, subpath, worktreeDir string) (string, error) {
	if strings.TrimSpace(sha) == "" {
		return "", fmt.Errorf("refusing to check out a worktree of %s: no commit was given to pin it to", repoURL)
	}
	if err := checkWorktreeSubpath(subpath); err != nil {
		return "", fmt.Errorf("refusing to check out a worktree of %s: %w", repoURL, err)
	}
	if strings.TrimSpace(worktreeDir) == "" {
		return "", fmt.Errorf("refusing to check out a worktree of %s: no directory was given to check it out into", repoURL)
	}

	repoDir, err := c.RepoDirForURL(repoURL)
	if err != nil {
		return "", fmt.Errorf("refusing to check out a worktree of %q: %w", repoURL, err)
	}
	if !isGitRepo(repoDir) {
		return "", fmt.Errorf("cannot check out %s at %s: %w (expected it at %s — run `ctxloom deps pull`)",
			subpath, sha, ErrNoCloneForWorktree, repoDir)
	}

	// The clone's .git is shared by every worktree taken off it, and `worktree
	// add`/`prune` write there — so this serializes on the CLONE's lock, the
	// same one clone/fetch take, not on the worktree directory.
	unlock := lockCloneDir(repoDir)
	defer unlock()

	if err := c.ensureWorktreeAt(ctx, repoDir, worktreeDir, sha); err != nil {
		return "", err
	}
	if err := runGit(ctx, worktreeDir, "sparse-checkout set", nil,
		"sparse-checkout", "set", "--no-cone", "--", subpath); err != nil {
		return "", fmt.Errorf("narrow the worktree at %s to %s: %w", worktreeDir, subpath, err)
	}
	// Re-run AFTER the sparse patterns are in place: `worktree add
	// --no-checkout` leaves the worktree with no files at all, and a
	// sparse-checkout set on an unpopulated worktree updates the patterns
	// without materializing anything. This is the call that writes the bytes.
	if err := runGit(ctx, worktreeDir, "checkout", nil,
		"checkout", "--detach", "--force", sha); err != nil {
		return "", fmt.Errorf("check %s out at %s in %s: %w", subpath, sha, worktreeDir, err)
	}

	dir := filepath.Join(worktreeDir, filepath.FromSlash(subpath))
	// A sparse checkout of a path the commit does not contain SUCCEEDS and
	// writes nothing — the silent-empty shape this project keeps paying for.
	// The pin is only honoured if the bytes are actually there.
	info, serr := os.Stat(dir)
	if serr != nil || !info.IsDir() {
		return "", fmt.Errorf("the commit %s of %s does not contain %s: the checkout succeeded and produced no such directory at %s "+
			"(the pin names a commit published before this bundle, or under a different layout)", sha, repoURL, subpath, dir)
	}
	return dir, nil
}

// ensureWorktreeAt guarantees worktreeDir is a registered worktree of repoDir.
// An existing one is REUSED (the caller's checkout then moves it to sha, which
// is what makes advancing a pin a git operation rather than a delete-and-refetch);
// anything else occupying the path is cleared and registered fresh.
func (c *RepoCache) ensureWorktreeAt(ctx context.Context, repoDir, worktreeDir, sha string) error {
	if isGitWorktree(worktreeDir) {
		return nil
	}
	if err := os.RemoveAll(worktreeDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("clear %s before checking a worktree out into it: %w", worktreeDir, err)
	}
	if err := os.MkdirAll(filepath.Dir(worktreeDir), 0o755); err != nil {
		return fmt.Errorf("create the cache directory for %s: %w", worktreeDir, err)
	}
	// Prune first: a worktree directory deleted from underneath git (a wiped
	// cache, an interrupted run) leaves its registration behind in the clone,
	// and `worktree add` then refuses the path as already registered.
	if err := runGit(ctx, repoDir, "worktree prune", nil, "worktree", "prune"); err != nil {
		return fmt.Errorf("prune stale worktree registrations in %s: %w", repoDir, err)
	}
	if err := runGit(ctx, repoDir, "worktree add", nil,
		"worktree", "add", "--no-checkout", "--detach", "--", worktreeDir, sha); err != nil {
		return fmt.Errorf("add a worktree of %s at %s pinned to %s: %w", repoDir, worktreeDir, sha, err)
	}
	return nil
}

// isGitWorktree reports whether dir is a LINKED git worktree.
//
// The test is that .git is a FILE: a worktree's .git is a pointer file naming
// its clone's gitdir, where a clone's own .git is a directory. isGitRepo asks
// the opposite question, and the two must not be confused — a worktree handed
// to a clone path (or the reverse) fails deep inside git with a message about
// neither.
func isGitWorktree(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && !info.IsDir()
}

// checkWorktreeSubpath rejects a sparse-checkout path that is not a plain,
// repository-relative, forward-slash directory path.
//
// The path reaches git as a sparse pattern and is joined onto the worktree root
// to name the directory a reader is then pointed at, so an absolute path or a
// traversal segment would select — and hand back — a directory outside the
// worktree entirely.
func checkWorktreeSubpath(subpath string) error {
	if strings.TrimSpace(subpath) == "" {
		return errors.New("no repository path was given to check out")
	}
	if path.IsAbs(subpath) || filepath.IsAbs(subpath) {
		return fmt.Errorf("the repository path %q is absolute; it must be relative to the repository root", subpath)
	}
	if subpath != path.Clean(subpath) {
		return fmt.Errorf("the repository path %q is not in cleaned form", subpath)
	}
	for _, seg := range strings.Split(subpath, "/") {
		if seg == ".." {
			return fmt.Errorf("the repository path %q escapes the repository root", subpath)
		}
	}
	return nil
}
