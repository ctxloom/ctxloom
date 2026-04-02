package paths

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Cache Directory Tests (formerly Ephemeral)
// =============================================================================
// The cache directory holds regeneratable content: bundles, vendor, context, memory.
// These can be deleted and re-fetched from remotes.

func TestCacheDir_Constant(t *testing.T) {
	// Cache directory should be named "cache" (renamed from "ephemeral")
	assert.Equal(t, "cache", CacheDir)
}

func TestGetCacheDir(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache", GetCacheDir(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/cache", GetCacheDir("/project/.ctxloom"))
}

func TestBundlesPath_InCache(t *testing.T) {
	// Bundles should be under cache/bundles/
	assert.Equal(t, ".ctxloom/cache/bundles", BundlesPath(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/cache/bundles", BundlesPath("/project/.ctxloom"))
}

func TestVendorPath_InCache(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache/vendor", VendorPath(".ctxloom"))
}

func TestContextPath_InCache(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache/context", ContextPath(".ctxloom"))
}

func TestMemoryPath_InCache(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache/memory", MemoryPath(".ctxloom"))
}

func TestPluginsPath_InCache(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache/plugins", PluginsPath(".ctxloom"))
}

// =============================================================================
// Root-Level Persistent Items Tests
// =============================================================================
// Persistent items (config, remotes, lock, profiles) should be at .ctxloom root,
// NOT under a nested "persistent/" directory.

func TestConfigPath_AtRoot(t *testing.T) {
	// Config should be at root: .ctxloom/config.yaml
	// NOT: .ctxloom/persistent/config.yaml
	assert.Equal(t, ".ctxloom/config.yaml", ConfigPath(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/config.yaml", ConfigPath("/project/.ctxloom"))
}

func TestRemotesPath_AtRoot(t *testing.T) {
	assert.Equal(t, ".ctxloom/remotes.yaml", RemotesPath(".ctxloom"))
}

func TestLockPath_AtRoot(t *testing.T) {
	assert.Equal(t, ".ctxloom/lock.yaml", LockPath(".ctxloom"))
}

func TestProfilesPath_AtRoot(t *testing.T) {
	// Profiles directory should be at root: .ctxloom/profiles/
	// NOT: .ctxloom/persistent/profiles/
	assert.Equal(t, ".ctxloom/profiles", ProfilesPath(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/profiles", ProfilesPath("/project/.ctxloom"))
}

// =============================================================================
// Default Path Tests
// =============================================================================

func TestDefaultAppDir(t *testing.T) {
	assert.Equal(t, ".ctxloom", DefaultAppDir())
}

func TestDefaultRemotesPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/remotes.yaml", DefaultRemotesPath())
}

func TestDefaultLockPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/lock.yaml", DefaultLockPath())
}

func TestDefaultVendorPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/cache/vendor", DefaultVendorPath())
}
