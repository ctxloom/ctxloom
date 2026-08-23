// Package paths provides the home-rooted and in-tree path conventions the
// task store shares with ctxloom. Data layout is unchanged by the extraction:
// task logs (ModeHome), the project registry, and the project marker all live
// where ctxloom put them, under ~/.ctxloom and <projectDir>/.ctxloom. A
// ModeRepo (repo-homed) task log is the one exception — see Mode and
// RepoTasksLogPath — living instead at <projectDir>/.taskloom/tasks.jsonl, a
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
	// the advisory-lock sidecar (RepoTasksFileName + ".lock") is gitignored.
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

// RepoTasksLogPath resolves <repoRoot>/RepoDirName/RepoTasksFileName
// (.taskloom/tasks.jsonl). No project-id is consulted — the repo path is the
// store's identity in this mode. repoRoot must already be redirected through
// any linked-worktree boundary by the caller (see projectroot.TaskStoreRoot);
// an empty one errors rather than resolving against the process's cwd.
func RepoTasksLogPath(repoRoot string) (string, error) {
	if repoRoot == "" {
		return "", fmt.Errorf("repo-homed task store: no project root resolved")
	}
	return filepath.Join(repoRoot, RepoDirName, RepoTasksFileName), nil
}

// HomeTasksLogPath resolves ~/.ctxloom/tasks/<project-id>.jsonl. The id is
// validated as a single clean path segment first: it arrives from an in-tree
// marker file, --project, or CTXLOOM_PROJECT_ID, none trusted to be
// traversal-free, and this is where it becomes a filesystem path.
func HomeTasksLogPath(projectID string) (string, error) {
	if err := ValidateProjectID(projectID); err != nil {
		return "", err
	}
	root, err := HomeTasksDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, projectID+TasksLogExt), nil
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
		if !projectIDRune(r) {
			return fmt.Errorf("project id %q contains an invalid character %q", id, r)
		}
	}
	// ".." alone is already covered by the Contains scan; only "." needs its
	// own equality, since it carries no doubled dot.
	if id == "." || strings.Contains(id, "..") {
		return fmt.Errorf("project id %q is not a valid path segment", id)
	}
	if strings.HasPrefix(id, ".") {
		return fmt.Errorf("project id %q must not start with a dot", id)
	}
	return nil
}

// projectIDRune reports whether r may appear in a project id: ASCII
// alphanumerics plus '-', '_' and '.'. Everything else — separators, "..",
// control characters, spaces, and every non-ASCII rune — is rejected, which
// is what keeps a crafted id from steering a write outside ~/.ctxloom/tasks.
func projectIDRune(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	case r == '-' || r == '_' || r == '.':
		return true
	default:
		return false
	}
}

// ProjectMarkerPath returns <projectDir>/.ctxloom/project-id — the in-tree
// marker carrying the project's stable project-id. An empty projectDir errors
// rather than resolving against the process's cwd, exactly as
// RepoTasksLogPath does: a cwd-relative ".ctxloom/project-id" is the REAL
// marker of whatever tree the process happens to sit in, so silently
// resolving it reads or overwrites another project's identity.
func ProjectMarkerPath(projectDir string) (string, error) {
	if projectDir == "" {
		return "", fmt.Errorf("project marker: no project directory resolved")
	}
	return filepath.Join(projectDir, AppDirName, projectMarkerFileName), nil
}
