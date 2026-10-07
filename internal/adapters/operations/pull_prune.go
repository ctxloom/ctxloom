package operations

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// errCheckoutRootNotOwned: the bundle checkout root, or the clone cache its
// checkouts belong to, is not physically inside this project's app directory.
var errCheckoutRootNotOwned = errors.New("the bundle checkout cache is not this project's own directory (a symlink points it elsewhere)")

// gitdirPrefix opens a linked worktree's .git file.
const gitdirPrefix = "gitdir:"

// pruneAfterPull deletes the bundle checkouts the active lockfile no longer
// names (pruneUnlockedCheckouts) and returns the directories it deleted. A
// failure warns and prunes nothing further: pruning is housekeeping after a
// pull that already succeeded, and must never fail it.
func pruneAfterPull(appDir string, lockManager *remote.LockfileManager) []string {
	lock, err := lockManager.Load()
	if err != nil {
		clidiag.Warn("ctxloom", "not pruning bundle checkouts: the lockfile cannot be read: %v", err)
		return nil
	}
	pruned, err := pruneUnlockedCheckouts(appDir, lock)
	if err != nil {
		clidiag.Warn("ctxloom", "not pruning bundle checkouts: %v", err)
	}
	return pruned
}

// pruneUnlockedCheckouts deletes every bundle checkout under appDir's checkout
// root whose bundle lock does not name, and returns the directories deleted.
//
// WHY THERE IS NO CROSS-PROJECT REGISTRY: the checkout root
// (paths.CacheBundlesPath), the clone cache (paths.ReposCachePath) and the
// lockfile (paths.LockPath) are all rooted at the one app directory, so the
// lock read here is the only lock that can name a checkout under this root.
// That holds only while the cache is physically the project's own, so a root
// or clone cache that a symlink points elsewhere — where another project's
// lock may name the checkouts — prunes nothing (errCheckoutRootNotOwned), and
// a checkout registered to a clone outside this project's clone cache is
// left to whoever owns that clone.
//
// CONTAINMENT: the walk never follows a symlink (a symlinked entry is not a
// directory to WalkDir), only directories named with remote.WorktreeDirSuffix
// are candidates, and each is resolved and held inside the resolved root
// before it is removed.
func pruneUnlockedCheckouts(appDir string, lock *remote.Lockfile) ([]string, error) {
	root := paths.CacheBundlesPath(appDir)
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	realRoot, err := ownedCacheDir(appDir, root)
	if err != nil {
		return nil, err
	}
	realClones, err := ownedCacheDir(appDir, paths.ReposCachePath(appDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil // no clone, so no checkout can be this project's
	} else if err != nil {
		return nil, err
	}
	keep := lockedCheckouts(appDir, lock)

	var pruned []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if path == root || !d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), remote.WorktreeDirSuffix) {
			return nil
		}
		if !keep.Has(filepath.Clean(path)) && ownedCheckout(path, realRoot, realClones) {
			if err := os.RemoveAll(path); err != nil {
				return fmt.Errorf("remove the bundle checkout %s: %w", path, err)
			}
			pruned = append(pruned, path)
		}
		return fs.SkipDir // a checkout's contents are git's, never a nested checkout
	})
	return pruned, err
}

// ownedCacheDir resolves dir, a cache directory under appDir, and returns it
// only when it is physically where appDir puts it: no symlink between appDir
// and dir moves it elsewhere.
func ownedCacheDir(appDir, dir string) (string, error) {
	realApp, err := filepath.EvalSymlinks(appDir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(appDir, dir)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return "", err
	}
	if real != filepath.Join(realApp, rel) {
		return "", fmt.Errorf("%w: %s resolves to %s", errCheckoutRootNotOwned, dir, real)
	}
	return real, nil
}

// lockedCheckouts is the checkout directory of every bundle lock names.
func lockedCheckouts(appDir string, lock *remote.Lockfile) collections.Set[string] {
	keep := collections.NewSet[string]()
	for key := range lock.Bundles {
		ref, err := remote.ParseReference(string(key))
		if err != nil {
			continue
		}
		if dir, err := ref.LocalWorktreePath(appDir); err == nil {
			keep.Add(filepath.Clean(dir))
		}
	}
	return keep
}

// ownedCheckout reports whether dir is a linked git worktree that resolves
// inside realRoot and whose .git file names a gitdir inside realClones —
// a checkout this project's own clone cache owns. Anything it cannot prove
// is not owned.
func ownedCheckout(dir, realRoot, realClones string) bool {
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || !strictlyInside(realRoot, real) {
		return false
	}
	dotGit := filepath.Join(dir, ".git")
	info, err := os.Lstat(dotGit)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	raw, err := os.ReadFile(dotGit)
	if err != nil {
		return false
	}
	gitdir, ok := strings.CutPrefix(string(bytes.TrimSpace(raw)), gitdirPrefix)
	if !ok {
		return false
	}
	gitdir = strings.TrimSpace(gitdir)
	if !filepath.IsAbs(gitdir) {
		gitdir = filepath.Join(dir, gitdir)
	}
	realGitdir, err := filepath.EvalSymlinks(gitdir)
	return err == nil && strictlyInside(realClones, realGitdir)
}

// strictlyInside reports whether path lies under parent and is not parent.
func strictlyInside(parent, path string) bool {
	rel, err := filepath.Rel(parent, path)
	return err == nil && rel != "." && !isOutsideRel(rel)
}
