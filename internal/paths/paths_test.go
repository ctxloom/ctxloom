package paths

import (
	"os"
	"path/filepath"
	"strings"
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
// Harp Session Directory Tests
// =============================================================================

func TestHarpPlanPath_InSessionDir(t *testing.T) {
	assert.Equal(t, ".plan.md", PlanFileExt)

	harpDir, err := HarpDir("swift-amber-falcon")
	if err != nil {
		t.Skipf("home dir unavailable: %v", err)
	}
	planPath, err := HarpPlanPath("swift-amber-falcon", "v1-removal")
	assert.NoError(t, err)
	// Plans are .plan.md files directly in the harp dir (next to tasks.md),
	// so a session can hold multiple distinctly named plans.
	assert.Equal(t, filepath.Join(harpDir, "v1-removal.plan.md"), planPath)
	assert.True(t, strings.HasSuffix(planPath, filepath.Join("sessions", "swift-amber-falcon", "v1-removal.plan.md")))
}

func TestSessionIndexPath_InSessionsRoot(t *testing.T) {
	root, err := HomeSessionsDir()
	if err != nil {
		t.Skipf("home dir unavailable: %v", err)
	}
	got, err := SessionIndexPath()
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "index.yaml"), got)
	assert.True(t, strings.HasSuffix(got, filepath.Join("sessions", "index.yaml")))
}

func TestHarpEssencePath_InHarpDir(t *testing.T) {
	harpDir, err := HarpDir("swift-amber-falcon")
	if err != nil {
		t.Skipf("home dir unavailable: %v", err)
	}
	got, err := HarpEssencePath("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(harpDir, "essence.md"), got)
}

// TestProjectSessionsDir covers the consolidated project-sessions resolver,
// including the cwd fallback the old truncated cmd/memory.go variant dropped.
func TestProjectSessionsDir(t *testing.T) {
	// Configured app dir wins.
	assert.Equal(t, filepath.Join("/app", "sessions"), ProjectSessionsDir("/app"))

	// Empty app dir falls back to <cwd>/.ctxloom/sessions — the behavior the
	// truncated variant lacked. We compare against the live cwd.
	got := ProjectSessionsDir("")
	if wd, err := os.Getwd(); err == nil {
		assert.Equal(t, filepath.Join(wd, AppDirName, SessionsDir), got)
	} else {
		assert.Equal(t, filepath.Join(AppDirName, SessionsDir), got)
	}
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
