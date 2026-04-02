// Package paths provides shared path constants for ctxloom.
package paths

import "path/filepath"

const (
	// AppDirName is the name of the ctxloom directory.
	AppDirName = ".ctxloom"

	// PersistentDir is the subdirectory for persistent data (config, remotes, lock).
	PersistentDir = "persistent"

	// EphemeralDir is the subdirectory for ephemeral data (bundles, vendor, context).
	EphemeralDir = "ephemeral"

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
)

// GetPersistentDir returns the persistent subdirectory path for the given app path.
func GetPersistentDir(appPath string) string {
	return filepath.Join(appPath, PersistentDir)
}

// GetEphemeralDir returns the ephemeral subdirectory path for the given app path.
func GetEphemeralDir(appPath string) string {
	return filepath.Join(appPath, EphemeralDir)
}

// ConfigPath returns the path to the config file.
func ConfigPath(appPath string) string {
	return filepath.Join(GetPersistentDir(appPath), ConfigFileName+".yaml")
}

// RemotesPath returns the path to the remotes file.
func RemotesPath(appPath string) string {
	return filepath.Join(GetPersistentDir(appPath), RemotesFileName+".yaml")
}

// LockPath returns the path to the lock file.
func LockPath(appPath string) string {
	return filepath.Join(GetPersistentDir(appPath), LockFileName+".yaml")
}

// BundlesPath returns the path to the bundles directory.
func BundlesPath(appPath string) string {
	return filepath.Join(GetEphemeralDir(appPath), BundlesDir)
}

// VendorPath returns the path to the vendor directory.
func VendorPath(appPath string) string {
	return filepath.Join(GetEphemeralDir(appPath), VendorDir)
}

// ContextPath returns the path to the context directory.
func ContextPath(appPath string) string {
	return filepath.Join(GetEphemeralDir(appPath), ContextDir)
}

// ProfilesPath returns the path to the profiles directory.
func ProfilesPath(appPath string) string {
	return filepath.Join(GetPersistentDir(appPath), ProfilesDir)
}

// MemoryPath returns the path to the memory directory.
func MemoryPath(appPath string) string {
	return filepath.Join(GetEphemeralDir(appPath), MemoryDir)
}

// PluginsPath returns the path to the plugins directory.
func PluginsPath(appPath string) string {
	return filepath.Join(GetEphemeralDir(appPath), PluginsDir)
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
