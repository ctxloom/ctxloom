package isolation

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
// The common dir itself is gitCommonDirMount's; this sits beside it.
func gitPointerMounts(rt Runtime, dir, scratchRoot string) ([]mount, error) {
	dotGit := filepath.Join(dir, ".git")
	admin, ok, err := readGitfile(dotGit)
	if err != nil || !ok || !filepath.IsAbs(admin) {
		return nil, err
	}
	m := rt.mapper()
	mappedAdmin, err := m.toContainer(admin)
	if err != nil {
		return nil, fmt.Errorf("git admin dir %s has no route into the container: %w", admin, err)
	}
	if mappedAdmin == admin {
		return nil, nil
	}
	backPointer := filepath.Join(admin, "gitdir")
	targets, err := mapAll(m, dotGit, backPointer)
	if err != nil {
		return nil, fmt.Errorf("git pointer for %s has no route into the container: %w", dir, err)
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		return nil, fmt.Errorf("git pointer scratch: %w", err)
	}
	pointerFile := filepath.Join(scratchRoot, "git-pointer")
	backFile := filepath.Join(scratchRoot, "git-backpointer")
	if err := os.WriteFile(pointerFile, []byte(gitdirPrefix+mappedAdmin+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git pointer: %w", err)
	}
	if err := os.WriteFile(backFile, []byte(targets[0]+"\n"), 0o644); err != nil {
		return nil, fmt.Errorf("git back-pointer: %w", err)
	}
	return []mount{rt.expose(pointerFile, targets[0], true), rt.expose(backFile, targets[1], true)}, nil
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

// mapAll maps each host path through m, failing on the first it cannot route.
func mapAll(m pathMapper, hosts ...string) ([]string, error) {
	out := make([]string, len(hosts))
	for i, h := range hosts {
		c, err := m.toContainer(h)
		if err != nil {
			return nil, err
		}
		out[i] = c
	}
	return out, nil
}
