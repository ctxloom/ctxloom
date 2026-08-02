package projectroot

import (
	"errors"
	"fmt"
	iofs "io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// worktreesDirName is the fixed path segment git uses for a linked
// worktree's per-worktree state directory. Per gitrepository-layout(5), a
// linked worktree's `.git` is a FILE containing:
//
//	gitdir: <commondir>/worktrees/<name>
//
// while the main worktree's `.git` is a directory (the commondir itself). A
// submodule's `.git` is also a file, but shaped `<commondir>/modules/<name>`
// -- no "worktrees" segment -- which is what lets DetectWorktree tell a
// linked worktree apart from a submodule without shelling out to git.
const worktreesDirName = "worktrees"

// errNoDir is returned when a caller names no directory at all. An empty dir
// is not "the current directory": filepath.Join("", x) yields the bare
// relative x, so an unnamed directory silently becomes whichever working
// directory the process happens to hold, and the answer changes with the
// launch cwd. Every function here exists to pin a project root down, so
// guessing at one is the opposite of the contract.
var errNoDir = errors.New("projectroot: no directory named (empty dir)")

// WorktreeInfo describes what dir's `.git` entry resolved to.
type WorktreeInfo struct {
	// Linked is true when dir is the root of a LINKED git worktree, as
	// opposed to the main worktree (.git is a directory), a submodule
	// (.git is a file but not worktree-shaped), or a plain non-repo
	// directory (no .git at all).
	Linked bool

	// MainRoot is the linked worktree's main-worktree root, derived purely
	// from the gitdir pointer text -- NOT yet verified to exist. Empty
	// unless Linked.
	MainRoot string

	// MainRootExists reports whether MainRoot actually stat'd as a readable
	// directory. False alongside a non-empty MainRoot means the gitdir
	// pointer is stale (the main worktree moved, or was deleted without
	// `git worktree remove`/`prune`).
	MainRootExists bool
}

// DetectWorktree inspects dir's `.git` entry (file or directory, if any) and
// classifies it. It reads the filesystem directly rather than shelling out to
// git, matching this package's existing style (see WorkDir/FromEnv), and
// operates on fs so it is exercised the same way in tests and in production
// (afero.NewOsFs()).
//
// It reports Linked=false, with no error, for every "not a linked worktree"
// shape: no .git at all, .git is a directory (the main worktree, or a plain
// non-worktree repo), or .git is a file that isn't a recognized/worktree-
// shaped gitdir pointer (e.g. a submodule). Those are exactly the cases this
// feature must leave unaffected (see internal/config's findAppDir /
// worktreeSignpost, the sole caller as of this writing).
//
// It returns a non-nil error when dir is empty (see errNoDir), and when dir's
// `.git` entry cannot be STAT'd or READ for any reason other than plain
// absence (permission denied, I/O error) -- distinct faults from "not a
// linked worktree," which callers surface rather than silently swallow. Only
// "the entry does not exist" means "not a worktree": a fault that reads as
// absence fails OPEN in the direction that loses data, since a linked
// worktree misclassified as an ordinary directory keeps a task store nobody
// running from the primary checkout will ever read.
func DetectWorktree(fs afero.Fs, dir string) (WorktreeInfo, error) {
	if dir == "" {
		return WorktreeInfo{}, errNoDir
	}
	dotGit := filepath.Join(dir, ".git")
	fi, statErr := fs.Stat(dotGit)
	if statErr != nil {
		if errors.Is(statErr, iofs.ErrNotExist) {
			return WorktreeInfo{}, nil // no .git here -- not a worktree root at all
		}
		return WorktreeInfo{}, fmt.Errorf("stat %s: %w", dotGit, statErr)
	}
	if fi.IsDir() {
		return WorktreeInfo{}, nil // the main worktree, or a plain repo
	}

	data, readErr := afero.ReadFile(fs, dotGit)
	if readErr != nil {
		return WorktreeInfo{}, fmt.Errorf("read %s: %w", dotGit, readErr)
	}
	pointer, ok := parseGitdirPointer(string(data))
	if !ok {
		return WorktreeInfo{}, nil // not a recognized `gitdir:` pointer
	}
	if !filepath.IsAbs(pointer) {
		pointer = filepath.Join(dir, pointer)
	}
	pointer = filepath.Clean(pointer)

	// Linked worktree shape: <commondir>/worktrees/<name>. A submodule's
	// gitdir points at <commondir>/modules/<name> instead -- no "worktrees"
	// segment -- so this is what disambiguates the two.
	worktreesParent := filepath.Dir(pointer)
	if filepath.Base(worktreesParent) != worktreesDirName {
		return WorktreeInfo{}, nil // e.g. a submodule -- not a worktree
	}
	commonDir := filepath.Dir(worktreesParent)

	// mainRoot=filepath.Dir(commonDir) only names a real MAIN
	// WORKTREE when commonDir is the standard "<main>/.git" layout. For a
	// bare clone (`git clone --bare url repo.git; git worktree add ../wt`)
	// or `git init --separate-git-dir=...`, commonDir is a bare repo/
	// separate gitdir with NO enclosing worktree, and filepath.Dir(commonDir)
	// is just some existing, unrelated parent directory -- silently
	// "resolving" as a plausible-looking main root that is not one. Treat
	// this exactly like a stale pointer: Linked, but with no derivable main
	// root, so TaskStoreRoot's existing MainRootExists==false branch (which
	// already fails loud and names `ctxloom init` as the remedy) handles it,
	// rather than a new, distinct failure mode.
	if filepath.Base(commonDir) != ".git" {
		return WorktreeInfo{Linked: true}, nil
	}
	mainRoot := filepath.Dir(commonDir)

	exists := false
	if info, err := fs.Stat(mainRoot); err == nil && info.IsDir() {
		exists = true
	}
	return WorktreeInfo{Linked: true, MainRoot: mainRoot, MainRootExists: exists}, nil
}

// parseGitdirPointer extracts the path from a `.git` file's `gitdir: <path>`
// line. Pure string parsing, no filesystem access, so every line-ending /
// whitespace variant git itself writes is trivial to unit test directly.
func parseGitdirPointer(contents string) (path string, ok bool) {
	for _, line := range strings.Split(contents, "\n") {
		line = strings.TrimRight(line, "\r")
		rest, found := strings.CutPrefix(line, "gitdir:")
		if !found {
			continue
		}
		return strings.TrimSpace(rest), true
	}
	return "", false
}
