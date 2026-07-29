// Package paths provides the home-rooted and in-tree path conventions the
// task store shares with ctxloom. Data layout is unchanged by the extraction:
// task logs (ModeHome), the project registry, and the project marker all live
// where ctxloom put them, under ~/.ctxloom and <projectDir>/.ctxloom. A
// ModeRepo (repo-homed) task log is the one exception — see Mode and
// TasksLogPath — living instead at <projectDir>/.taskloom/tasks.jsonl, a
// deliberately separate dot-dir from .ctxloom because it (and its
// config.yaml, see internal/taskloom/config) is meant to be COMMITTED, unlike
// anything under .ctxloom/* (private working state, gitignored).
package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/paths"
)

const (
	// AppDirName is the name of the ctxloom directory; the task store shares
	// it rather than minting a parallel dot-dir. It is an ALIAS of
	// internal/paths.AppDirName, not a second declaration of ".ctxloom":
	// independent literals in two packages let a drift in either silently stop
	// TaskStoreRoot's documented opt-out from matching the directory
	// `ctxloom init` creates, with no error at either end. Both packages are
	// import leaves apart from this edge, so there is no cycle to break.
	AppDirName = paths.AppDirName

	// projectsDir is the home-rooted subdirectory holding the project-identity
	// registry that maps a stable project-id to its current path.
	projectsDir = "projects"

	// IndexFileName is the name of the registry index file.
	IndexFileName = "index.yaml"

	// TasksDir is the home-rooted subdirectory holding per-project append-only
	// task logs, one <project-id>.jsonl per project.
	TasksDir = "tasks"

	// projectMarkerFileName is the in-tree marker carrying a project's stable
	// project-id. It lives at <projectDir>/.ctxloom/project-id and is gitignored:
	// private working-state identity must never ride a distributable tree.
	projectMarkerFileName = "project-id"

	// TasksLogExt is the suffix for a per-project task log file.
	TasksLogExt = ".jsonl"

	// RepoDirName is the in-tree directory a REPO-HOMED task store (and its
	// config.yaml) lives under — deliberately its own dot-dir, never nested
	// under AppDirName (.ctxloom): .ctxloom/* is gitignored PRIVATE working
	// state (see the root .gitignore), while .taskloom/config.yaml and (in
	// repo-homed mode) .taskloom/tasks.jsonl are meant to be COMMITTED. Only
	// the filelock sidecar (RepoTasksFileName + ".lock") is gitignored.
	RepoDirName = ".taskloom"

	// RepoTasksFileName is the repo-homed task log's file name, alongside
	// RepoDirName's config.yaml.
	RepoTasksFileName = "tasks.jsonl"
)

// Mode selects where a project's task-store log is homed. See
// internal/taskloom/config for how it is resolved from taskloom's own
// layered config (home < project < env < flag); this package only knows the
// two path conventions each mode resolves to.
type Mode string

const (
	// ModeHome is today's (pre-homing) behavior: the log lives PRIVATELY
	// under ~/.ctxloom/tasks/<project-id>.jsonl, keyed by a minted/registered
	// project-id. Never shared by a clone.
	ModeHome Mode = "home"
	// ModeRepo checks the log into the project tree itself, at
	// <repoRoot>/.taskloom/tasks.jsonl — SHAREABLE, travels with clones. No
	// project-id is minted or consulted in this mode: the repo path itself
	// is the store's identity.
	ModeRepo Mode = "repo"
)

// homeProjectsDir returns ~/.ctxloom/projects — the home-rooted directory
// holding the project-identity registry.
func homeProjectsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName, projectsDir), nil
}

// ProjectRegistryPath returns ~/.ctxloom/projects/index.yaml — the registry
// mapping each stable project-id to its current path.
func ProjectRegistryPath() (string, error) {
	root, err := homeProjectsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, IndexFileName), nil
}

// HomeTasksDir returns ~/.ctxloom/tasks — the home-rooted directory holding
// per-project append-only task logs.
func HomeTasksDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName, TasksDir), nil
}

// TasksLogPath resolves a project's task log path for the given homing mode.
//
// ModeRepo needs repoRoot (the project root, already redirected through any
// linked-worktree-to-primary-checkout boundary by the caller — see
// projectroot.TaskStoreRoot) and ignores projectID entirely: the repo path
// itself is the store's identity in this mode, so no id is validated or
// consulted. The result is
// <repoRoot>/RepoDirName/RepoTasksFileName (.taskloom/tasks.jsonl).
//
// ModeHome — also the default for "", so every pre-homing caller keeps its
// exact prior behavior unchanged — needs projectID and ignores repoRoot,
// resolving ~/.ctxloom/tasks/<project-id>.jsonl exactly as before. The id is
// validated as a single clean path segment first: it reaches here from an
// in-tree marker file, --project, and CTXLOOM_PROJECT_ID, none of which are
// trusted to be traversal-free, and this is the chokepoint where the id
// becomes a filesystem path (the lock file is derived from it too).
func TasksLogPath(mode Mode, repoRoot, projectID string) (string, error) {
	switch mode {
	case ModeRepo:
		if repoRoot == "" {
			return "", fmt.Errorf("repo-homed task store: no project root resolved")
		}
		return filepath.Join(repoRoot, RepoDirName, RepoTasksFileName), nil
	case ModeHome, "":
		if err := ValidateProjectID(projectID); err != nil {
			return "", err
		}
		root, err := HomeTasksDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(root, projectID+TasksLogExt), nil
	default:
		return "", fmt.Errorf("task store: unknown homing mode %q", mode)
	}
}

// ValidateProjectID reports whether id is safe to use as the single-segment
// filename component of a task log path. A project-id is normally a harp name
// ("swift-amber-falcon"), but it also arrives from the in-tree marker file,
// --project, and CTXLOOM_PROJECT_ID — none trusted to be a clean path segment.
// Rejecting separators, "..", leading dots, and control/space characters keeps
// a crafted id (e.g. a committed marker of "../../../home/user/.bashrc") from
// steering a write outside ~/.ctxloom/tasks.
func ValidateProjectID(id string) error {
	if id == "" {
		return fmt.Errorf("project id is empty")
	}
	if len(id) > 255 {
		return fmt.Errorf("project id is too long (%d bytes)", len(id))
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '_' || r == '.':
		default:
			return fmt.Errorf("project id %q contains an invalid character %q", id, r)
		}
	}
	if id == "." || id == ".." || strings.Contains(id, "..") {
		return fmt.Errorf("project id %q is not a valid path segment", id)
	}
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("project id %q must not start with a dot", id)
	}
	return nil
}

// ProjectMarkerPath returns <projectDir>/.ctxloom/project-id — the in-tree
// marker carrying the project's stable project-id.
func ProjectMarkerPath(projectDir string) string {
	return filepath.Join(projectDir, AppDirName, projectMarkerFileName)
}
