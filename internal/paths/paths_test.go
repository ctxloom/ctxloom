package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	assert.Equal(t, ".ctxloom/cache", CachePath(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/cache", CachePath("/project/.ctxloom"))
}

func TestCacheBundlesPath_InCache(t *testing.T) {
	// Bundles should be under cache/bundles/
	assert.Equal(t, ".ctxloom/cache/bundles", CacheBundlesPath(".ctxloom"))
	assert.Equal(t, "/project/.ctxloom/cache/bundles", CacheBundlesPath("/project/.ctxloom"))
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

// TestTriggerCacheDir_HomeRootedUnderCache pins the trigger verdict cache to
// ~/.ctxloom/cache/triggers — home-rooted (never inside a project tree) and
// under the general CacheDir convention (safe to delete), distinct from
// taskloom's own ~/.ctxloom/tasks store.
func TestTriggerCacheDir_HomeRootedUnderCache(t *testing.T) {
	testsupport.Isolate(t)
	got, err := TriggerCacheDir()
	assert.NoError(t, err)
	assert.True(t, strings.HasSuffix(got, filepath.Join(AppDirName, CacheDir, "triggers")))
}

func TestSessionIndexPath_InSessionsRoot(t *testing.T) {
	testsupport.Isolate(t)
	root, err := HomeSessionsDir()
	assert.NoError(t, err)
	got, err := SessionIndexPath()
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "index.yaml"), got)
	assert.True(t, strings.HasSuffix(got, filepath.Join("sessions", "index.yaml")))
}

func TestHarpEssencePath_InHarpDir(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := HarpDir("swift-amber-falcon")
	assert.NoError(t, err)
	got, err := HarpEssencePath("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(harpDir, "essence.md"), got)
}

// TestProjectSessionsDir covers the consolidated project-sessions resolver,
// including the cwd fallback the old truncated cmd/memory.go variant dropped.
func TestProjectSessionsDir(t *testing.T) {
	dir := testsupport.ProjectDir(t)

	// Configured app dir wins.
	assert.Equal(t, filepath.Join("/app", "sessions"), ProjectSessionsDir("/app"))

	// Empty app dir falls back to <cwd>/.ctxloom/sessions — the behavior the
	// truncated variant lacked. We compare against the isolated project cwd.
	assert.Equal(t, filepath.Join(dir, AppDirName, SessionsDir), ProjectSessionsDir(""))
}

// =============================================================================
// Coordinator State Directory Tests
// =============================================================================

// TestHomeCoordDir_HomeRootedCoordSegment pins ~/.ctxloom/coord — the root
// internal/agentcoord/coord and discover both resolve project coordinator
// state under (see CoordProjectStateDir). The literal "coord" (not
// CoordDirName) is deliberate: a mutation to the constant's VALUE must still
// fail this.
func TestHomeCoordDir_HomeRootedCoordSegment(t *testing.T) {
	testsupport.Isolate(t)
	got, err := HomeCoordDir()
	assert.NoError(t, err)
	assert.True(t, strings.HasSuffix(got, filepath.Join(AppDirName, "coord")))
}

// TestCoordProjectStateDir_UnderHomeCoordDir pins the per-project state dir
// as a direct child of HomeCoordDir, keyed by the caller's (already
// sanitized) project key.
func TestCoordProjectStateDir_UnderHomeCoordDir(t *testing.T) {
	testsupport.Isolate(t)
	root, err := HomeCoordDir()
	assert.NoError(t, err)
	got, err := CoordProjectStateDir("proj-key")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "proj-key"), got)
}

// TestHomeLocksDir_HomeRootedLocksSegment pins ~/.ctxloom/locks — the
// directory the isolation package's container lock-path fix mounts whole
// into a container (internal/lm/isolation's sessionStateMounts), and the
// directory filelock.HomePathFor derives its lock sidecars under. The
// literal "locks" (not HomeLocksDirName) is deliberate, mirroring
// TestHomeCoordDir_HomeRootedCoordSegment: a mutation to the constant's
// VALUE must still fail this.
func TestHomeLocksDir_HomeRootedLocksSegment(t *testing.T) {
	home := testsupport.Isolate(t)
	got, err := HomeLocksDir()
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(home, AppDirName, "locks"), got)
}

// =============================================================================
// Default Path Tests
// =============================================================================

func TestDefaultRemotesPath(t *testing.T) {
	assert.Equal(t, ".ctxloom/remotes.yaml", DefaultRemotesPath())
}

// =============================================================================
// Per-session state layout (§6d): ephemeral/ vs persist/ under the harp dir
// =============================================================================

func TestHarpStateDirs_Layout(t *testing.T) {
	home := testsupport.Isolate(t)
	root := filepath.Join(home, ".ctxloom", "sessions", "swift-amber-falcon")

	eph, err := HarpEphemeralDir("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "ephemeral"), eph)

	persist, err := HarpPersistDir("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "persist"), persist)

	store, err := HarpTranscriptStoreDir("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "persist", "transcripts"), store,
		"the transcript store nests under persist/: it must survive teardown")

	canonical, err := HarpCanonicalTranscriptPath("swift-amber-falcon")
	assert.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "persist", "transcript.jsonl"), canonical,
		"the canonical transcript is a FILE under persist/, distinct from the transcripts/ bind-mount dir")
}

// =============================================================================
// ResolveHarpCanonicalTranscriptPath: transcript.acp.jsonl -> transcript.jsonl
// rename back-compat (readers must still find a pre-rename session's file).
// =============================================================================

func TestResolveHarpCanonicalTranscriptPath_NoFile_ReturnsCurrentName(t *testing.T) {
	testsupport.Isolate(t)
	p, err := ResolveHarpCanonicalTranscriptPath("never-captured-harp")
	require.NoError(t, err)
	current, err := HarpCanonicalTranscriptPath("never-captured-harp")
	require.NoError(t, err)
	assert.Equal(t, current, p, "with neither file present, resolve must still yield the current-name path so a caller's own stat cleanly reports not-found")
}

func TestResolveHarpCanonicalTranscriptPath_CurrentOnly(t *testing.T) {
	testsupport.Isolate(t)
	current, err := HarpCanonicalTranscriptPath("current-only-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Dir(current), 0o755))
	require.NoError(t, os.WriteFile(current, []byte("{}\n"), 0o644))

	p, err := ResolveHarpCanonicalTranscriptPath("current-only-harp")
	require.NoError(t, err)
	assert.Equal(t, current, p)
}

func TestResolveHarpCanonicalTranscriptPath_LegacyOnly_FallsBack(t *testing.T) {
	testsupport.Isolate(t)
	dir, err := HarpPersistDir("legacy-only-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	legacy := filepath.Join(dir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(legacy, []byte("{}\n"), 0o644))

	p, err := ResolveHarpCanonicalTranscriptPath("legacy-only-harp")
	require.NoError(t, err)
	assert.Equal(t, legacy, p,
		"a session captured before the rename has ONLY transcript.acp.jsonl on disk — it must still resolve")
}

func TestResolveHarpCanonicalTranscriptPath_BothPresent_PrefersCurrent(t *testing.T) {
	testsupport.Isolate(t)
	dir, err := HarpPersistDir("both-present-harp")
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	current := filepath.Join(dir, "transcript.jsonl")
	legacy := filepath.Join(dir, "transcript.acp.jsonl")
	require.NoError(t, os.WriteFile(current, []byte(`{"current":true}`+"\n"), 0o644))
	require.NoError(t, os.WriteFile(legacy, []byte(`{"legacy":true}`+"\n"), 0o644))

	p, err := ResolveHarpCanonicalTranscriptPath("both-present-harp")
	require.NoError(t, err)
	assert.Equal(t, current, p, "the current name wins when both exist — nothing writes the legacy name post-rename")
}

// TestHarpDir_RefusesTraversingNames is the class gate at the path
// chokepoint: every harp-derived path (essence, ephemeral, canonical
// transcript, the harp dir itself) is built from HarpDir, so a name that is
// not one path component must be refused HERE rather than at each caller's
// MkdirAll. The assertion is on the RESOLVED PATH, not on the error string:
// whatever HarpDir returns must still live under the sessions root.
func TestHarpDir_RefusesTraversingNames(t *testing.T) {
	testsupport.Isolate(t)
	root, err := HomeSessionsDir()
	require.NoError(t, err)

	for _, name := range []string{"..", "../..", "../../etc", "a/b", `a\b`, "", " lead"} {
		got, err := HarpDir(name)
		if err == nil {
			t.Errorf("HarpDir(%q) = %q, nil; want an error", name, got)
			continue
		}
		assert.Empty(t, got, "HarpDir(%q) must not return a path alongside its error", name)
	}

	// A real harp still resolves, directly under the sessions root.
	ok, err := HarpDir("swift-amber-falcon")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(root, "swift-amber-falcon"), ok)
}

// TestHarpDerivedPaths_RefuseTraversingNames extends the gate to every path
// helper layered on HarpDir — the point of putting validation at the
// chokepoint is that none of them can be reached with a traversing name.
func TestHarpDerivedPaths_RefuseTraversingNames(t *testing.T) {
	testsupport.Isolate(t)
	helpers := map[string]func(string) (string, error){
		"HarpDir":                            HarpDir,
		"HarpEssencePath":                    HarpEssencePath,
		"HarpEphemeralDir":                   HarpEphemeralDir,
		"ResolveHarpCanonicalTranscriptPath": ResolveHarpCanonicalTranscriptPath,
		"HarpEngineTranscriptLinkPath": func(harp string) (string, error) {
			return HarpEngineTranscriptLinkPath(harp, "claude-code", "sess-1")
		},
	}
	for label, fn := range helpers {
		got, err := fn("../../escape")
		assert.Error(t, err, "%s must refuse a traversing harp name", label)
		assert.NotContains(t, got, "escape", "%s leaked a traversed path: %q", label, got)
	}
}

// TestHarpEngineTranscriptLinkPath_NamesEngineAndSession pins the on-disk
// name every per-vendor-log symlink gets: engine-transcript-<engine>-<session
// id>.jsonl, directly under the harp dir root (a sibling of the retired
// bare transcript.jsonl, not nested under persist/).
func TestHarpEngineTranscriptLinkPath_NamesEngineAndSession(t *testing.T) {
	testsupport.Isolate(t)
	harpDir, err := HarpDir("swift-amber-falcon")
	require.NoError(t, err)

	got, err := HarpEngineTranscriptLinkPath("swift-amber-falcon", "claude-code", "abc-123")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(harpDir, "engine-transcript-claude-code-abc-123.jsonl"), got)
	assert.True(t, strings.HasPrefix(filepath.Base(got), EngineTranscriptLinkPrefix))
}

// TestHarpEngineTranscriptLinkPath_RequiresEngineAndSessionID pins that a
// missing engine or session id is a refusal, not a malformed path built from
// an empty component (which would silently collide with a differently-named
// binding, e.g. "engine-transcript--abc.jsonl" for two different engines that
// both happened to have an empty name).
func TestHarpEngineTranscriptLinkPath_RequiresEngineAndSessionID(t *testing.T) {
	testsupport.Isolate(t)
	for _, tc := range []struct{ engine, sessionID string }{
		{"", "abc-123"},
		{"claude-code", ""},
		{"", ""},
	} {
		got, err := HarpEngineTranscriptLinkPath("swift-amber-falcon", tc.engine, tc.sessionID)
		assert.Error(t, err, "engine=%q session=%q must be refused", tc.engine, tc.sessionID)
		assert.Empty(t, got, "engine=%q session=%q must not return a path alongside its error", tc.engine, tc.sessionID)
	}
}
