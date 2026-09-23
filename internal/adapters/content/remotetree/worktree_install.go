package remotetree

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/adapters/content"
	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// nonExecutableMode and executableMode are the two modes a bundle tree's files
// are ever installed at. A tree declares executability as a BOOLEAN (see
// content.DeclaredExecutable), so there is no third mode to carry.
const (
	nonExecutableMode fs.FileMode = 0o644
	executableMode    fs.FileMode = 0o755
)

// WorktreeInstaller is this package as a remote.TreeInstallFunc: git checks the
// pinned tree out, and this then reconciles the checkout's modes with what the
// tree DECLARES.
//
// WHY THE MODES NEED RECONCILING AT ALL. ctxloom publishes every file at 0644
// (remote.GitPublisher writes them that way), so the commit records 100644 even
// for a skill's scripts, and a checkout faithfully reproduces that. A skill's
// package manifest — its trust preimage and the mode it is materialized at —
// is read from the files on disk, so left alone every checked-out script would
// be delivered non-executable. The declaration has to be stamped onto the
// checkout before anything reads it.
//
// The declaration is read from the CHECKOUT rather than from a fetched tree,
// which is what lets a pin move without a fetch: the sidecars are files in the
// same commit, so `checkout --detach <new sha>` plus this reconciliation is a
// complete installation.
func WorktreeInstaller(cache *remote.RepoCache) remote.TreeInstallFunc {
	return func(ctx context.Context, repoURL, sha, subpath, worktreeDir string) (string, error) {
		dir, err := cache.EnsureSparseWorktree(ctx, repoURL, sha, subpath, worktreeDir)
		if err != nil {
			return "", err
		}
		if err := applyDeclaredModesOnDisk(dir, subpath); err != nil {
			return "", fmt.Errorf("content/remotetree: reconciling installed modes for %s at %s: %w", subpath, sha, err)
		}
		return dir, nil
	}
}

// Compile-time proof that this package satisfies the install seam, matching the
// one PullTreeFetcher carries for the fetch seam.
var _ remote.TreeInstallFunc = WorktreeInstaller(nil)

// applyDeclaredModesOnDisk sets every file in an installed bundle tree to the
// mode its own sidecars declare.
//
// A file the publisher committed EXECUTABLE but did not DECLARE executable is
// installed non-executable and reported, exactly as it was when the installer
// wrote the bytes itself. That divergence is otherwise invisible: the file lands
// runnable, the manifest the tree generates says it is not, and verification
// fails somewhere that cannot name the missing declaration.
func applyDeclaredModesOnDisk(dir, repoPath string) error {
	files, err := readTreeBytes(dir)
	if err != nil {
		return err
	}
	declared, err := content.DeclaredExecutable(files)
	if err != nil {
		return err
	}
	for rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(full)
		if err != nil {
			return fmt.Errorf("stat %s: %w", rel, err)
		}
		committedExecutable := info.Mode().Perm()&0o111 != 0
		want := nonExecutableMode
		if declared[rel] {
			want = executableMode
		}
		if committedExecutable && !declared[rel] {
			warnUndeclaredExecutable(repoPath + "/" + rel)
		}
		if info.Mode().Perm() == want {
			continue
		}
		if err := os.Chmod(full, want); err != nil {
			return fmt.Errorf("set the declared mode on %s: %w", rel, err)
		}
	}
	return nil
}

// readTreeBytes reads an installed tree as forward-slash-keyed bytes, the shape
// content.DeclaredExecutable takes.
//
// Symlinks are SKIPPED rather than followed: a checkout can contain one, and
// following it would read — and then chmod — a file outside the tree entirely.
func readTreeBytes(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !d.Type().IsRegular() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return rerr
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the installed tree at %s: %w", dir, err)
	}
	return out, nil
}

// warnUndeclaredExecutable reports a file the publisher committed executable
// that the package does not DECLARE executable.
//
// It names the repository path, the declaration that is missing, and the
// consequence, because downstream everything is consistent and quiet: the file
// is installed 0644, the manifest agrees, verification passes, and the model is
// handed a script it cannot run.
func warnUndeclaredExecutable(repoPath string) {
	clidiag.Warn("ctxloom", "%s is committed executable upstream but the package does not declare it executable, "+
		"so it was installed DECLARED NON-EXECUTABLE (mode 0644) and will not run. "+
		"A mode bit is not portable and is not covered by the signature, so the declaration is what travels: "+
		"add it to the executable: list in the package's .meta.yaml sidecar and re-publish.", repoPath)
}
