// Package paths provides shared path constants for ctxloom.
package paths

import "path/filepath"

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
)

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
