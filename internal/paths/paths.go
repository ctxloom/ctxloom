// Package paths provides shared path constants for ctxloom.
package paths

import (
	"os"
	"path/filepath"
)

const (
	// AppDirName is the name of the ctxloom directory.
	AppDirName = ".ctxloom"

	// CacheDir is the subdirectory for cached/regeneratable data (bundles, vendor, context).
	// These can be deleted and re-fetched from remotes.
	CacheDir = "cache"

	// ConfigFileName is the name of the config file (without extension).
	ConfigFileName = "config"

	// BundlesDir is the subdirectory for bundles.
	BundlesDir = "bundles"

	// VendorDir is the subdirectory for vendored dependencies.
	VendorDir = "vendor"

	// ContextDir is the subdirectory for context files.
	ContextDir = "context"

	// RemotesFileName is the name of the remotes file (without extension).
	RemotesFileName = "remotes"

	// LockFileName is the name of the lock file (without extension).
	LockFileName = "lock"

	// ProfilesDir is the subdirectory for profiles.
	ProfilesDir = "profiles"

	// MemoryDir is the subdirectory for memory/session files.
	MemoryDir = "memory"

	// PluginsDir is the subdirectory for plugins.
	PluginsDir = "plugins"

	// ReposCacheDir is the subdirectory for cached git repo clones.
	ReposCacheDir = "repos"

	// SessionsDir is the subdirectory for per-session state (index, harp dirs).
	SessionsDir = "sessions"

	// IndexFileName is the name of the home-rooted session index file.
	IndexFileName = "index.yaml"

	// EssenceFileName is the name of a harp's distilled session essence.
	EssenceFileName = "essence.md"

	// TasksFileName is the name of the task store file.
	TasksFileName = "tasks.md"

	// PlanFileExt is the suffix for a session's plan documents. Plans live
	// directly in the harp session directory (alongside tasks.md) as
	// <descriptive-name>.plan.md files; a session may hold several.
	PlanFileExt = ".plan.md"
)

// HomeSessionsDir returns ~/.ctxloom/sessions — the home-rooted directory
// that holds the session index and per-harp session dirs. This is the
// single source of truth for the sessions root; both the task store and the
// memory compactor resolve harp paths through it so they cannot diverge.
func HomeSessionsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, AppDirName, SessionsDir), nil
}

// SessionIndexPath returns ~/.ctxloom/sessions/index.yaml — the home-rooted
// session index that binds harp names to backend sessions.
func SessionIndexPath() (string, error) {
	root, err := HomeSessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, IndexFileName), nil
}

// HarpDir returns ~/.ctxloom/sessions/<harp>/. Errors when the home dir
// can't be resolved; callers fall back to the legacy layout in that case.
func HarpDir(harp string) (string, error) {
	root, err := HomeSessionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, harp), nil
}

// HarpEssencePath returns ~/.ctxloom/sessions/<harp>/essence.md — the distilled
// session essence for a harp-named session.
func HarpEssencePath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, EssenceFileName), nil
}

// ProjectSessionsDir returns the project-rooted directory holding distilled
// session .md files. It is distinct from the home-rooted HomeSessionsDir: this
// is per-project state under the app dir. Resolution prefers the configured app
// dir, then <cwd>/.ctxloom/sessions, then a bare relative .ctxloom/sessions when
// even the working directory can't be resolved.
func ProjectSessionsDir(appDir string) string {
	if appDir != "" {
		return filepath.Join(appDir, SessionsDir)
	}
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, AppDirName, SessionsDir)
	}
	return filepath.Join(AppDirName, SessionsDir)
}

// HarpTasksPath returns ~/.ctxloom/sessions/<harp>/tasks.md — the active
// task store for a harp-named session.
func HarpTasksPath(harp string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, TasksFileName), nil
}

// HarpPlanPath returns ~/.ctxloom/sessions/<harp>/<name>.plan.md — a plan
// document for a harp-named session. Plans sit directly in the session
// directory next to tasks.md; name is the descriptive base (no extension),
// and a session may have several distinctly named plans.
func HarpPlanPath(harp, name string) (string, error) {
	dir, err := HarpDir(harp)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name+PlanFileExt), nil
}

// GetCacheDir returns the cache subdirectory path for the given app path.
// Cache contains regeneratable content: bundles, vendor, context, memory.
func GetCacheDir(appPath string) string {
	return filepath.Join(appPath, CacheDir)
}

// ConfigPath returns the path to the config file (at appPath root).
func ConfigPath(appPath string) string {
	return filepath.Join(appPath, ConfigFileName+".yaml")
}

// RemotesPath returns the path to the remotes file (at appPath root).
func RemotesPath(appPath string) string {
	return filepath.Join(appPath, RemotesFileName+".yaml")
}

// LockPath returns the path to the lock file (at appPath root).
func LockPath(appPath string) string {
	return filepath.Join(appPath, LockFileName+".yaml")
}

// ProfilesPath returns the path to the profiles directory (at appPath root).
func ProfilesPath(appPath string) string {
	return filepath.Join(appPath, ProfilesDir)
}

// BundlesPath returns the path to the bundles directory (under cache/).
func BundlesPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), BundlesDir)
}

// VendorPath returns the path to the vendor directory (under cache/).
func VendorPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), VendorDir)
}

// ContextPath returns the path to the context directory (under cache/).
func ContextPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), ContextDir)
}

// MemoryPath returns the path to the memory directory (under cache/).
func MemoryPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), MemoryDir)
}

// PluginsPath returns the path to the plugins directory (under cache/).
func PluginsPath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), PluginsDir)
}

// ReposCachePath returns the path to the repos cache directory (under cache/).
func ReposCachePath(appPath string) string {
	return filepath.Join(GetCacheDir(appPath), ReposCacheDir)
}

// DefaultAppDir returns the default app directory path relative to current directory.
func DefaultAppDir() string {
	return AppDirName
}

// DefaultRemotesPath returns the default remotes path relative to current directory.
func DefaultRemotesPath() string {
	return RemotesPath(AppDirName)
}

// DefaultLockPath returns the default lock path relative to current directory.
func DefaultLockPath() string {
	return LockPath(AppDirName)
}

// DefaultVendorPath returns the default vendor path relative to current directory.
func DefaultVendorPath() string {
	return VendorPath(AppDirName)
}
