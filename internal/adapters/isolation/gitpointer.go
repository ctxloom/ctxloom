package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
)

// gitDirMounts is every mount a checkout with a `gitdir:` POINTER FILE (a
// linked worktree or submodule, whose git data lives OUTSIDE the mounted
// checkout) needs for git to work in the container — THIS checkout's git data,
// and not the other checkouts'. Shared by the worktree base (whose .git is
// always a pointer file) and the host base (only when the live project is
// itself a linked worktree — see gitdirMirrorMounts).
//
// The common dir is mounted whole and READ-WRITE, at the runtime's mapping of
// it: commits, fetches and branch updates rewrite config, packed-refs and refs
// by lock-and-rename INSIDE it, and a rename onto a file bind-mounted on its
// own fails (EBUSY), while a file git creates beside such mounts would land in
// the container's own layer and be lost. So the common dir cannot be assembled
// from its children one by one.
//
// Its worktrees/ registry is then MASKED (gitRegistryMask), because the
// registry is the one part of the common dir that belongs to OTHER checkouts
// and that git in here would act on: `git worktree prune`, and gc's automatic
// prune, delete every registration whose back-pointer names a checkout that
// is not there — and no other checkout is mounted into this container. The
// host's registrations would be deleted through the mount.
//
// Hiding the registry has a price: git in the container cannot see which
// branches other checkouts have checked out, so its refusal to check out, or
// delete, a branch that is in use elsewhere does not fire here.
func gitDirMounts(ctx context.Context, rt Runtime, g git.Git, dir, scratchRoot string) ([]mount, error) {
	common, err := g.CommonDir(ctx, dir)
	if err != nil {
		return nil, fmt.Errorf("resolve git common dir for container gitdir mount: %w", err)
	}
	// expose (not bind(common, common, ...)) routes through the runtime's
	// target rule — the SAME translation the project root gets.
	commonMount, err := rt.paths().expose(common, false)
	if err != nil {
		return nil, fmt.Errorf("git common dir %s has no route into the container: %w", common, err)
	}
	registry, err := gitRegistryMask(rt, common, dir, scratchRoot)
	if err != nil {
		return nil, err
	}
	pointers, err := gitPointerMounts(rt, dir, scratchRoot)
	if err != nil {
		return nil, err
	}
	return append(append([]mount{commonMount}, registry...), pointers...), nil
}

// gitRegistryMask covers <common>/worktrees with an empty directory from the
// run's scratch, then mounts this checkout's own admin dir back at its place
// in it, read-write: that dir is where git keeps this checkout's HEAD, index
// and per-worktree refs. Nothing when the common dir has no registry.
//
// The mask is READ-ONLY so a registration git tries to create in here (a
// nested `git worktree add`) fails loudly instead of landing in scratch that
// the host never sees. That is also why the admin dir's mountpoint is
// created in the mask beforehand: the runtime cannot create it in a read-only
// source, and would create it as root under a rootful daemon if it could.
func gitRegistryMask(rt Runtime, common, dir, scratchRoot string) ([]mount, error) {
	registry := filepath.Join(common, "worktrees")
	present, err := gitRegistryPresent(registry)
	if err != nil || !present {
		return nil, err
	}
	admin, own, err := ownAdminDir(dir, registry)
	if err != nil {
		return nil, err
	}
	mask := filepath.Join(scratchRoot, "git-worktrees")
	mountpoint := mask
	if own {
		mountpoint = filepath.Join(mask, filepath.Base(admin))
	}
	if err := os.MkdirAll(mountpoint, 0o755); err != nil {
		return nil, fmt.Errorf("git worktree registry mask: %w", err)
	}
	seam := rt.paths()
	target, err := seam.targetFor(registry)
	if err != nil {
		return nil, fmt.Errorf("git worktree registry %s has no route into the container: %w", registry, err)
	}
	mounts := []mount{seam.bind(mask, target, true)}
	if !own {
		return mounts, nil
	}
	adminMount, err := seam.expose(filepath.Clean(admin), false)
	if err != nil {
		return nil, fmt.Errorf("git admin dir %s has no route into the container: %w", admin, err)
	}
	return append(mounts, adminMount), nil
}

// gitRegistryPresent reports whether registry is a directory. A missing
// registry, or a non-directory in its place, is not an error: there is
// nothing to mask.
func gitRegistryPresent(registry string) (bool, error) {
	switch info, err := os.Stat(registry); {
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("git worktree registry: %w", err)
	default:
		return info.IsDir(), nil
	}
}

// ownAdminDir resolves the admin dir dir's .git gitfile names (relative to
// dir when relative, and NOT cleaned) and whether it is registered in
// registry — own is false when dir has no gitfile.
func ownAdminDir(dir, registry string) (admin string, own bool, err error) {
	admin, ok, err := readGitfile(filepath.Join(dir, ".git"))
	if err != nil {
		return "", false, err
	}
	if ok && !filepath.IsAbs(admin) {
		admin = filepath.Join(dir, admin)
	}
	return admin, ok && filepath.Dir(filepath.Clean(admin)) == registry, nil
}

// gitdirPrefix opens a gitfile: a `.git` FILE whose content names the real
// git dir (gitrepository-layout(5)).
const gitdirPrefix = "gitdir: "

// gitPointerMounts makes a linked checkout's git pointers true inside the
// container. `git worktree add` writes two absolute HOST paths: the
// checkout's .git file names its admin dir (<common>/worktrees/<name>), and
// that admin dir's gitdir file names the checkout back. Where the runtime
// names those paths differently in the container (a Windows host), git there
// cannot open the one and reads the other as a checkout that is gone — which
// `git worktree prune`, and gc's automatic prune, act on through the
// read-write common-dir mount. So each is shadowed by a READ-ONLY generated
// copy naming the mapped path; the host's own files are never touched.
//
// Nothing is mounted where the pointer already resolves in the container:
// no .git, a .git directory, a relative pointer (the mapping preserves
// prefixes), or a mapping that names the admin dir as written (identity).
func gitPointerMounts(rt Runtime, dir, scratchRoot string) ([]mount, error) {
	dotGit := filepath.Join(dir, ".git")
	admin, ok, err := readGitfile(dotGit)
	if err != nil || !ok || !filepath.IsAbs(admin) {
		return nil, err
	}
	seam := rt.paths()
	mappedAdmin, err := seam.targetFor(admin)
	if err != nil {
		return nil, fmt.Errorf("git admin dir %s has no route into the container: %w", admin, err)
	}
	if mappedAdmin == admin {
		return nil, nil
	}
	backPointer := filepath.Join(admin, "gitdir")
	targets, err := mapAll(seam, dotGit, backPointer)
	if err != nil {
		return nil, fmt.Errorf("git pointer for %s has no route into the container: %w", dir, err)
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return nil, fmt.Errorf("git pointer scratch: %w", err)
	}
	pointerFile := filepath.Join(scratchRoot, "git-pointer")
	backFile := filepath.Join(scratchRoot, "git-backpointer")
	if err := iox.WriteFileAtomic(pointerFile, []byte(gitdirPrefix+mappedAdmin+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git pointer: %w", err)
	}
	if err := iox.WriteFileAtomic(backFile, []byte(targets[0]+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git back-pointer: %w", err)
	}
	return []mount{seam.bind(pointerFile, targets[0], true), seam.bind(backFile, targets[1], true)}, nil
}

// readGitfile returns the git dir a .git FILE points to. ok is false for no
// .git or a .git directory: nothing points anywhere.
func readGitfile(dotGit string) (gitdir string, ok bool, err error) {
	b, err := os.ReadFile(dotGit)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "", false, nil
	case err != nil:
		if info, serr := os.Stat(dotGit); serr == nil && info.IsDir() {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read %s: %w", dotGit, err)
	}
	line, _, _ := strings.Cut(string(b), "\n")
	gitdir, found := strings.CutPrefix(strings.TrimRight(line, "\r"), gitdirPrefix)
	if !found || gitdir == "" {
		return "", false, fmt.Errorf("%s is not a gitdir pointer", dotGit)
	}
	return gitdir, true, nil
}

// mapAll routes each host path through s's target rule, failing on the first
// it cannot route.
func mapAll(s pathSeam, hosts ...string) ([]string, error) {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		c, err := s.targetFor(h)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}
