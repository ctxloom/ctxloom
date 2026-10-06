package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/git"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/spf13/afero"
)

// gitDirMounts is every mount a checkout with a `gitdir:` POINTER FILE (a
// linked worktree or submodule, whose git data lives OUTSIDE the mounted
// checkout) needs for git to work in the container — THIS checkout's git data,
// and not the other checkouts'. Shared by the worktree base (whose .git is
// always a pointer file) and the host base (when the live project is itself a
// linked worktree — see gitdirMirrorMounts, which masks a main checkout's
// registry with gitRegistryMask alone).
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
	// anchor (not a mount at common's own path) places it by the runtime's
	// placement policy — the SAME one the project root gets.
	commonMount, err := anchor(rt, common, false)
	if err != nil {
		return nil, fmt.Errorf("git common dir %s has no route into the container: %w", common, err)
	}
	registry, err := gitRegistryMask(rt, common, dir, scratchRoot)
	if err != nil {
		return nil, err
	}
	pointers, err := gitPointerMounts(rt, dir, common, scratchRoot)
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
	// The registry and the admin dir sit in the common dir, so the child names
	// both through the common dir's own mount.
	commonMount, err := anchor(rt, common, false)
	if err != nil {
		return nil, fmt.Errorf("git common dir %s has no route into the container: %w", common, err)
	}
	target, err := childPath(rt, registry, commonMount)
	if err != nil {
		return nil, fmt.Errorf("git worktree registry %s has no route into the container: %w", registry, err)
	}
	mounts := []mount{{Host: mask, Container: target, ReadOnly: true}}
	if !own {
		return mounts, nil
	}
	admin = filepath.Clean(admin)
	adminTarget, err := childPath(rt, admin, commonMount)
	if err != nil {
		return nil, fmt.Errorf("git admin dir %s has no route into the container: %w", admin, err)
	}
	return append(mounts, mount{Host: admin, Container: adminTarget}), nil
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
// container. `git worktree add` writes two absolute paths, as the process
// that ran it named them: the checkout's .git file names its admin dir
// (<common>/worktrees/<name>), and that admin dir's gitdir file names the
// checkout back. Where the child names those paths differently (a Windows
// host, or a placement policy that moves the checkout or the common dir), git
// there cannot open the one and reads the other as a checkout that is gone —
// which `git worktree prune`, and gc's automatic prune, act on through the
// read-write common-dir mount. So each is shadowed by a READ-ONLY generated
// copy naming the child's path (Crossing.ToChild through the checkout's and
// commonDir's mounts, which make them visible); the host's own files are
// never touched.
//
// Nothing is mounted where the pointer already resolves in the container:
// no .git, a .git directory, a relative pointer (it resolves wherever the
// placement keeps the checkout and the common dir at the same relative
// position, as every prefix-preserving policy does), or an admin dir the
// child names as written (identity).
func gitPointerMounts(rt Runtime, dir, commonDir, scratchRoot string) ([]mount, error) {
	dotGit := filepath.Join(dir, ".git")
	admin, ok, err := readGitfile(dotGit)
	if err != nil || !ok || !filepath.IsAbs(admin) {
		return nil, err
	}
	checkout, err := anchor(rt, dir, false)
	if err != nil {
		return nil, fmt.Errorf("git checkout %s has no route into the container: %w", dir, err)
	}
	common, err := anchor(rt, commonDir, false)
	if err != nil {
		return nil, fmt.Errorf("git common dir %s has no route into the container: %w", commonDir, err)
	}
	// The pointer text names the admin dir as the CHILD sees it, through the
	// common dir's mount; the back-pointer names the checkout's .git through
	// the checkout's.
	mappedAdmin, err := childPath(rt, admin, common)
	if err != nil {
		return nil, fmt.Errorf("git admin dir %s has no route into the container: %w", admin, err)
	}
	if mappedAdmin == admin {
		return nil, nil
	}
	backPointer := filepath.Join(admin, "gitdir")
	targets, err := childPaths(rt, []string{dotGit, backPointer}, checkout, common)
	if err != nil {
		return nil, fmt.Errorf("git pointer for %s has no route into the container: %w", dir, err)
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return nil, fmt.Errorf("git pointer scratch: %w", err)
	}
	pointerFile := filepath.Join(scratchRoot, "git-pointer")
	backFile := filepath.Join(scratchRoot, "git-backpointer")
	if err := safefs.WriteFile(afero.NewOsFs(), pointerFile, []byte(gitdirPrefix+mappedAdmin+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git pointer: %w", err)
	}
	if err := safefs.WriteFile(afero.NewOsFs(), backFile, []byte(targets[0]+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git back-pointer: %w", err)
	}
	return []mount{{Host: pointerFile, Container: targets[0], ReadOnly: true}, {Host: backFile, Container: targets[1], ReadOnly: true}}, nil
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

// childPaths names each controller path in the child whose mounts are
// mounts, failing on the first it cannot name.
func childPaths(rt Runtime, ctl []string, mounts ...mount) ([]string, error) {
	c, err := crossingOver(rt, mounts...)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(ctl))
	for i, p := range ctl {
		if out[i], _, err = c.ToChild(p); err != nil {
			return nil, err
		}
	}
	return out, nil
}
