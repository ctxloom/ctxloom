package configload

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/afero"
	"go.uber.org/zap"

	"github.com/ctxloom/ctxloom/internal/adapters/projectroot"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// findAppDir locates the .ctxloom directory.
// Priority:
//  1. CTXLOOM_ROOT override (when set and a valid directory)
//  2. Walk up from cwd looking for .ctxloom directory
//  3. Fall back to user home ~/.ctxloom directory
//
// Always returns a path (creates user home .ctxloom if needed).
func findAppDir(fs afero.Fs) (string, config.ConfigSource) {
	// CTXLOOM_ROOT is authoritative when valid: the user named the root
	// explicitly, so resolve config at $CTXLOOM_ROOT/.ctxloom and create it if
	// absent, mirroring the home fallback below. A failed MkdirAll warns and
	// continues — the path is still returned so the run isn't blocked.
	if root, ok := projectroot.FromEnv(fs); ok {
		appPath := filepath.Join(root, config.AppDirName)
		if err := fs.MkdirAll(appPath, 0755); err != nil {
			zap.L().Warn("failed to create CTXLOOM_ROOT .ctxloom directory", zap.String("path", appPath), zap.Error(err))
		}
		return appPath, config.SourceProject
	}

	// The walk-up-from-cwd loop below has one deliberate boundary: the OS
	// shared temp directory (os.TempDir(), typically /tmp). That directory is
	// multi-tenant scratch space, not a project root — anyone (another
	// process, another test run, a leftover from days ago) can have left a
	// `.ctxloom` sitting directly in it, and walking past a bare temp dir
	// with no boundary would silently adopt that unrelated directory as THIS
	// process's project config. The boundary check runs BEFORE the .ctxloom
	// stat for that one directory only — subdirectories under the temp root
	// (an ordinary t.TempDir(), a real project checked out under /tmp) are
	// still walked and still honor their OWN .ctxloom marker exactly as
	// before; only the temp root itself is excluded from consideration.
	tempRoot := filepath.Clean(os.TempDir())

	// Try to find project .ctxloom by walking up from cwd
	pwd, err := os.Getwd()
	if err == nil {
		if appPath, ok := walkUpForAppDir(fs, pwd, tempRoot); ok {
			return appPath, config.SourceProject
		}
	}

	// Fall back to user home ~/.ctxloom
	home, err := os.UserHomeDir()
	if err != nil {
		zap.L().Warn("failed to get home directory", zap.Error(err))
		return lastResortAppDir(fs, pwd), config.SourceProject
	}

	homeApp := filepath.Join(home, config.AppDirName)

	// Ensure the directory exists
	if err := fs.MkdirAll(homeApp, 0755); err != nil {
		zap.L().Warn("failed to create home .ctxloom directory", zap.Error(err))
	}

	return homeApp, config.SourceHome
}

// walkUpForAppDir walks from dir toward the filesystem root looking for a
// directory that carries its own .ctxloom, reporting the first one found.
//
// It stops at tempRoot — see findAppDir's boundary note — and signposts any
// linked git worktree it passes through on the way.
func walkUpForAppDir(fs afero.Fs, dir, tempRoot string) (string, bool) {
	// Loop condition (not an if/break at the top): reached the shared OS
	// temp root without finding a project .ctxloom anywhere beneath it —
	// stop here rather than resolving to whatever (if anything) lives at
	// tempRoot itself, and let the caller fall through to its home
	// fallback.
	for filepath.Clean(dir) != tempRoot {
		appPath := filepath.Join(dir, config.AppDirName)
		if info, err := fs.Stat(appPath); err == nil && info.IsDir() {
			return appPath, true
		}

		// dir has no .ctxloom of its own. If dir is the root of a LINKED
		// git worktree, that is a signpost, not a silent walk-past:
		// resolving straight through to some unrelated ancestor's (or
		// home's) .ctxloom would silently land the session on the wrong
		// project — empty config, no profiles, no agents (an earlier
		// revision had linked worktrees INHERIT the main worktree's project
		// identity; that inheritance design was withdrawn in favor of this
		// signpost).
		// worktreeSignpost records a fatal finding through strictness and
		// the walk continues exactly as it always has — the choke owners
		// (`ctxloom run`/`mcp`/`acp`) abort on it pre-launch unless
		// --degraded; management commands surface the stderr warning and
		// proceed on the fallback. The main worktree (.git is a
		// directory) and every non-worktree ancestor pass through
		// untouched.
		worktreeSignpost(fs, dir)

		parent := filepath.Dir(dir)
		if parent == dir {
			// Reached root
			break
		}
		dir = parent
	}
	return "", false
}

// lastResortAppDir answers when the home directory itself is unresolvable: the
// cwd's .ctxloom, resolved ABSOLUTELY and created, matching both of findAppDir's
// other returns. config.NewBuilder derives appRoot as filepath.Dir of this, so a
// relative result would resolve the whole project to "." and make every path
// built from it — bundles, agents, sessions, the config file — depend on
// whatever cwd the process holds when it is used. This is the branch reached
// when the environment is already degraded; it must not degrade the answer
// further. pwd is "" when os.Getwd() failed too.
func lastResortAppDir(fs afero.Fs, pwd string) string {
	appPath := filepath.Join(pwd, config.AppDirName)
	if pwd == "" {
		if abs, aerr := filepath.Abs(config.AppDirName); aerr == nil {
			appPath = abs
		} else {
			appPath = config.AppDirName
		}
	}
	if err := fs.MkdirAll(appPath, 0755); err != nil {
		zap.L().Warn("failed to create fallback .ctxloom directory", zap.String("path", appPath), zap.Error(err))
	}
	return appPath
}

// worktreeSignpost records a fatal ClassConfig finding when dir is the root of
// a LINKED git worktree carrying no .ctxloom of its own — naming the resolved
// main worktree root and both remediation paths (run from the main worktree,
// or `ctxloom init` here to make this worktree a deliberately separate
// project). FailOnce, because findAppDir runs on every Read and a process
// reads more than once (each Reload) — the finding must not stack up in one
// startup window. No-op (walk continues to today's fallback) when dir is not
// such a worktree root.
//
// A linked worktree WITH its own .ctxloom never reaches this call: the walk in
// findAppDir already returned on the .ctxloom check for that same dir. That is
// the one, load-bearing precedence rule for this feature — own .ctxloom always
// wins, no further worktree inspection.
func worktreeSignpost(fs afero.Fs, dir string) {
	info, err := projectroot.DetectWorktree(fs, dir)
	if err != nil {
		strictness.FailOnce(strictness.ClassConfig,
			"check permissions on the .git file in this directory",
			"%s: could not read git worktree metadata: %v", dir, err)
		return
	}
	if !info.Linked {
		return
	}
	if !info.MainRootExists {
		strictness.FailOnce(strictness.ClassConfig,
			fmt.Sprintf("restore the main worktree at %s, or prune this stale linked worktree (`git worktree prune` from a healthy checkout), or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
			"%s is a linked git worktree, but its main worktree at %s is missing or unreadable", dir, info.MainRoot)
		return
	}
	strictness.FailOnce(strictness.ClassConfig,
		fmt.Sprintf("run ctxloom from %s, or run `ctxloom init` here to make this worktree a deliberately separate project", info.MainRoot),
		"this is a linked git worktree of the project at %s (no .ctxloom of its own)", info.MainRoot)
}
