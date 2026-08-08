// Package operations tests for sync verify remote dependency synchronization.
//
// Sync is how ctxloom pulls remote bundles and profiles from git remotes.
// It scans config for remote references (anything with "/" like "github/bundle")
// and downloads missing items to the local .ctxloom directory.
//
// # What Constitutes a Remote Reference
//
// References are classified as remote if they contain:
//   - A slash (e.g., "github/bundle", "remote/path/bundle")
//   - A URL scheme (https://, git@, file://)
//
// References WITHOUT slashes are local (e.g., "my-bundle", "local-config").
//
// # Sync Behavior
//
// SyncDependencies operates with these semantics:
//   - By default, existing bundles are SKIPPED (incremental sync)
//   - With Force=true, existing bundles are re-downloaded (full sync)
//   - Missing items are pulled from the remote registry
//   - Errors on individual items don't fail the entire sync
//
// # Test Injection Patterns
//
// Tests inject dependencies to avoid network calls:
//   - FS: afero virtual filesystem for local storage
//   - Registry: Pre-configured remote registry
//   - Puller: Mock puller that records calls instead of making HTTP requests
//
// # SyncOnStartup
//
// SyncOnStartup is a specialized sync for LLM session startup. It checks
// if missing dependencies exist and pulls them. This runs automatically
// when `ctxloom run` starts an LLM session to ensure context is available.
package operations

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/collections"
)

// syncMockPuller is a test puller that records calls for sync tests.
// It implements the Puller interface without making real HTTP requests.
type syncMockPuller struct {
	pullCalls []syncMockPullCall
	err       error
}

type syncMockPullCall struct {
	ref  string
	opts remote.PullOptions
}

func (m *syncMockPuller) Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
	m.pullCalls = append(m.pullCalls, syncMockPullCall{ref: refStr, opts: opts})
	if m.err != nil {
		return nil, m.err
	}
	return &remote.PullResult{
		LocalPath:   opts.LocalDir + "/bundles/test/bundle.yaml",
		SHA:         "abc1234",
		Overwritten: false,
	}, nil
}

// recordedRetraction captures one RecordRetraction call for assertion.
type recordedRetraction struct {
	itemType  remote.ItemType
	ref       string
	retracted bool
	reason    string
	checkedAt time.Time
}

// syncMockRetractionPuller extends syncMockPuller with the RetractionChecker
// seam (operations.RetractionChecker) so tests can drive syncItem's
// ALREADY-INSTALLED retraction re-check without a real *remote.Puller/
// lockfile — the fix for the gap where an installed ref's retraction was
// never re-evaluated (this puller stands in for *remote.Puller, which
// implements the same two methods over a real fetcher + lockfile).
type syncMockRetractionPuller struct {
	syncMockPuller

	retracted bool
	reason    string
	checkedAt time.Time // echoed back by CheckRetraction; defaults to time.Now() below when zero
	checkErr  error
	recordErr error // returned by RecordRetraction, e.g. to drive a save-failure test

	checkCalls []string
	recorded   []recordedRetraction
}

func (m *syncMockRetractionPuller) CheckRetraction(_ context.Context, refStr string, _ remote.ItemType) (bool, string, time.Time, error) {
	m.checkCalls = append(m.checkCalls, refStr)
	if m.checkErr != nil {
		return false, "", time.Time{}, m.checkErr
	}
	checkedAt := m.checkedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	return m.retracted, m.reason, checkedAt, nil
}

func (m *syncMockRetractionPuller) RecordRetraction(itemType remote.ItemType, refStr string, retracted bool, reason string, checkedAt time.Time) error {
	m.recorded = append(m.recorded, recordedRetraction{itemType: itemType, ref: refStr, retracted: retracted, reason: reason, checkedAt: checkedAt})
	return m.recordErr
}

// ==========================================================================
// Reference classification tests
// ==========================================================================

// TestCollectRemoteReferences verifies that remote vs local references are
// correctly distinguished. This is the first step of sync - finding what
// needs to be downloaded.
func TestCollectRemoteReferences(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"https://github.com/test/ctxloom@bundles/go-tools", // Remote
					"local-bundle", // Local (no scheme)
					"https://gitlab.com/test/sec@bundles/security", // Remote
				},
				Parents: []string{
					// Bundle-profile parent: its underlying bundle is collected.
					"https://github.com/test/ctxloom@bundles/kit#profiles/parent",
				},
			},
			"local-only": {
				Bundles: []string{
					"my-local-bundle",
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	// Create the profiles directory
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	bundles, err := collectRemoteReferences(cfg, nil)
	if err != nil {
		t.Fatalf("collectRemoteReferences failed: %v", err)
	}

	// Two remote bundle refs plus the bundle behind the bundle-profile parent.
	if len(bundles) != 3 {
		t.Errorf("expected 3 remote bundles, got %d: %v", len(bundles), bundles)
	}
}

// TestCollectRemoteReferences_DefaultProfilesAreRoots verifies that remote
// refs in profiles.defaults are collected even when no local profile
// references them. This is the init-seeded default profile case: it resolves
// only through a lockfile entry, so pull must treat it as a dependency root
// or the first `ctxloom run` after init can never assemble it.
func TestCollectRemoteReferences_DefaultProfilesAreRoots(t *testing.T) {
	fs := afero.NewMemMapFs()

	seededDefaultBundle := "https://github.com/ctxloom/ctxloom-default@bundles/default"
	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents: map[string]agents.Agent{"default": {Profiles: []string{
			seededDefaultBundle + "#profiles/default", // bundle-profile default
			"go-dev", // local name — not a remote ref, stays out of the pull set
		}}},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
		AppPaths: []string{testBaseDir},
	})
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	bundles, err := collectRemoteReferences(cfg, nil)
	if err != nil {
		t.Fatalf("collectRemoteReferences failed: %v", err)
	}
	if len(bundles) != 1 || bundles[0] != seededDefaultBundle {
		t.Errorf("expected default bundle %q as the sole root, got %v", seededDefaultBundle, bundles)
	}

	// An explicit profile filter scopes the sync to those profiles only —
	// config defaults must not leak into a targeted sync.
	bundles, err = collectRemoteReferences(cfg, []string{"go-dev"})
	if err != nil {
		t.Fatalf("collectRemoteReferences (filtered) failed: %v", err)
	}
	if len(bundles) != 0 {
		t.Errorf("expected no roots for a targeted sync, got %v", bundles)
	}
}

// TestCollectRemoteReferences_RetiredProfileRefsSkipped verifies a ref in the
// retired top-level "@profiles/" grammar never enters the install plan — it
// cannot pull, so planning it walks the user into a confirmed install that then
// fails with "unknown item type". The rest of the profile's refs still collect
// (fault tolerance: skip the broken branch, keep what works).
func TestCollectRemoteReferences_RetiredProfileRefsSkipped(t *testing.T) {
	fs := afero.NewMemMapFs()

	validBundle := "https://github.com/test/ctxloom@bundles/go-tools"
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{validBundle},
				Parents: []string{
					// Retired top-level profile distribution grammar.
					"https://github.com/ctxloom/ctxloom-default@profiles/go-developer",
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	})
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	bundles, err := collectRemoteReferences(cfg, nil)
	if err != nil {
		t.Fatalf("collectRemoteReferences failed: %v", err)
	}
	if len(bundles) != 1 || bundles[0] != validBundle {
		t.Errorf("expected only %q collected, got %v", validBundle, bundles)
	}
}

// TestCollectRemoteReferences_RetiredDefaultProfileSkipped covers the config
// defaults root path: a retired "@profiles/" default must not become a
// dependency root either.
func TestCollectRemoteReferences_RetiredDefaultProfileSkipped(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"https://github.com/o/r@profiles/dev"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{},
		},
		AppPaths: []string{testBaseDir},
	})
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	bundles, err := collectRemoteReferences(cfg, nil)
	if err != nil {
		t.Fatalf("collectRemoteReferences failed: %v", err)
	}
	if len(bundles) != 0 {
		t.Errorf("expected no roots from a retired default ref, got %v", bundles)
	}
}

// TestIsRemoteReference tests the heuristic for remote detection.
//
// NON-OBVIOUS: A reference is considered remote if it has a slash OR looks
// like a URL. This means "github/bundle" is remote even though it's not a
// full URL - ctxloom expands it using the remote registry.
//
// The "profile:" prefix indicates a LOCAL profile reference, not remote.
// This is used to distinguish profile refs from bundle refs in parent lists.
func TestIsRemoteReference(t *testing.T) {
	tests := []struct {
		ref      string
		expected bool
	}{
		// Only scheme-qualified canonical URLs are remote now.
		{"https://github.com/owner/repo", true},
		{"git@github.com:owner/repo", true},
		{"file:///path/to/repo", true},
		// Short "repo/path" form is no longer a remote reference (eliminated).
		{"github/bundle", false},
		{"remote/path/bundle", false},
		{"local-bundle", false},
		{"my-bundle", false},
		// ctxloom:local and the profile: alias are local, not remote.
		{"ctxloom:local@bundles/foo", false},
		{"profile:personal/typescript-dev", false},
		{"profile:nested/deep/profile", false},
		{"profile:simple", false},
	}

	for _, tc := range tests {
		t.Run(tc.ref, func(t *testing.T) {
			result := isRemoteReference(tc.ref)
			if result != tc.expected {
				t.Errorf("isRemoteReference(%q) = %v, want %v", tc.ref, result, tc.expected)
			}
		})
	}
}

// ==========================================================================
// SyncDependencies tests
// ==========================================================================

// TestSyncDependencies_NoRemotes verifies that sync completes cleanly when
// there are only local bundles. Status is "empty" meaning no work needed.
func TestSyncDependencies_NoRemotes(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"local": {
				Bundles: []string{"local-bundle"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	// Create the profiles directory
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS: fs,
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	if result.Status != "empty" {
		t.Errorf("expected status 'empty', got %q", result.Status)
	}
}

func TestSyncDependencies_WithRemotes(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	// Create necessary directories
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	// Create registry with test remote
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockPuller{}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:       fs,
		Registry: registry,
		Puller:   puller,
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	if result.Status != "completed" && result.Status != "completed_with_errors" {
		t.Errorf("expected completed status, got %q", result.Status)
	}

	if result.Total != 1 {
		t.Errorf("expected 1 total item, got %d", result.Total)
	}

	// Should have called pull
	if len(puller.pullCalls) != 1 {
		t.Errorf("expected 1 pull call, got %d", len(puller.pullCalls))
	}
}

// TestSyncDependencies_PullOutputAvoidsStdout pins the MCP stdio invariant:
// sync runs inside the MCP server, whose stdout carries JSON-RPC, so every
// pull's informational output (lockfile warnings) must be routed to stderr. An
// unset PullOptions.Stdout defaults to os.Stdout inside Pull, which would
// corrupt the protocol stream.
func TestSyncDependencies_PullOutputAvoidsStdout(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	puller := &syncMockPuller{}

	_, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:       fs,
		Registry: registry,
		Puller:   puller,
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	if len(puller.pullCalls) != 1 {
		t.Fatalf("expected 1 pull call, got %d", len(puller.pullCalls))
	}
	assert.Same(t, os.Stderr, puller.pullCalls[0].opts.Stdout,
		"sync pulls must route informational output to stderr, never process stdout")
}

// TestSyncDependencies_SkipsExisting verifies incremental sync behavior.
//
// By default, sync does NOT re-pull items that are already installed. In the
// reference-only model nothing lives on disk, so "installed" means the active
// lockfile holds the canonical ref AND its content is retrievable from the
// clone cache — the same probe CheckMissingDependencies uses.
func TestSyncDependencies_SkipsExisting(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockPuller{}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        false,
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	// Should skip existing
	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped item, got %d", len(result.Skipped))
	}

	// Should not have called pull
	if len(puller.pullCalls) != 0 {
		t.Errorf("expected 0 pull calls, got %d", len(puller.pullCalls))
	}
}

// TestSyncDependencies_RetractedInstalledRef pins the fix for the gap this
// task closes: an ALREADY-INSTALLED ref must have its retraction status
// re-evaluated on every sync, not just on a fresh pull. Before the fix,
// syncItem's install-skip meant a retracted-after-the-fact bundle was
// reported "skipped" forever with no warning and no lockfile record — see
// tests/acceptance/features/j001500_corporate_signed.feature's retraction
// scenario, which exercises the same gap end to end through the real CLI.
func TestSyncDependencies_RetractedInstalledRef(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockRetractionPuller{retracted: true, reason: "compromised release"}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        false,
	})
	require.NoError(t, err)

	require.Len(t, result.Retracted, 1, "a retracted already-installed ref must be reported, not silently skipped")
	assert.Equal(t, "https://github.com/test/ctxloom@bundles/go-tools", result.Retracted[0].Reference)
	assert.Equal(t, "compromised release", result.Retracted[0].Error)
	assert.Empty(t, result.Skipped, "retracted takes the place of skipped, not alongside it")

	require.Len(t, puller.checkCalls, 1, "the lightweight retraction check must run for the installed ref")
	require.Len(t, puller.recorded, 1, "the verdict must be persisted so EffectiveTrust can read it back later")
	assert.True(t, puller.recorded[0].retracted)
	assert.Equal(t, "compromised release", puller.recorded[0].reason)
	assert.Empty(t, puller.pullCalls, "an already-installed ref's retraction re-check must not trigger a full Pull")
}

// TestSyncDependencies_NotRetractedInstalledRef proves the happy path of the
// same re-check is a no-op: an installed ref that is NOT retracted is still
// reported skipped, and RecordRetraction is still called (to clear any STALE
// retraction from a previous sync — RecordRetraction itself is a no-op when
// nothing would change, see Puller.RecordRetraction).
func TestSyncDependencies_NotRetractedInstalledRef(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockRetractionPuller{retracted: false}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        false,
	})
	require.NoError(t, err)

	assert.Empty(t, result.Retracted)
	assert.Len(t, result.Skipped, 1)
	assert.Len(t, puller.checkCalls, 1)
}

// TestSyncDependencies_UnreachableRemoteHonorsFallbackVerdict is the
// sync-layer half of the fail-stale fix: when the puller
// cannot reach the remote, Puller.CheckRetraction (the real implementation)
// falls back to the last recorded verdict instead of erroring — reflected
// here as the mock reporting a RETRACTED verdict stamped with a PAST
// checkedAt rather than "now" (exactly what a fallback, as opposed to a fresh
// check, produces). checkInstalledRetraction must plumb that verdict through
// to the sync result AND pass the SAME (past) checkedAt to RecordRetraction —
// bumping it to "now" would fabricate freshness the check never earned and
// silently defeat the 14-day staleness warning on every subsequent sync.
func TestSyncDependencies_UnreachableRemoteHonorsFallbackVerdict(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	fallbackCheckedAt := time.Now().UTC().Add(-20 * 24 * time.Hour) // stale, but still the truth
	puller := &syncMockRetractionPuller{
		retracted: true,
		reason:    "shipped an incorrect deploy step",
		checkedAt: fallbackCheckedAt,
	}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        false,
	})
	require.NoError(t, err)

	require.Len(t, result.Retracted, 1, "an unreachable-remote fallback verdict of RETRACTED must still be reported, exactly like a fresh one")
	assert.Equal(t, "shipped an incorrect deploy step", result.Retracted[0].Error)

	require.Len(t, puller.recorded, 1)
	assert.True(t, puller.recorded[0].retracted)
	assert.True(t, puller.recorded[0].checkedAt.Equal(fallbackCheckedAt),
		"the fallback's own (past) checkedAt must be persisted verbatim, never bumped to now")
}

// checkInstalledRetraction used to discard RecordRetraction's own
// error (`_ = rc.RecordRetraction(...)`) — a failure to PERSIST a genuine
// retraction verdict is a different failure than "couldn't reach the remote
// to check" (which this function is deliberately fault-tolerant about); it
// silently drops a security improvement (or a live retraction) with zero
// diagnostic. The sync itself must still succeed (best-effort, never blocks),
// but the failure must be visible.
func TestSyncDependencies_RecordRetractionSaveFailureIsWarnedNotSwallowed(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"}},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockRetractionPuller{retracted: false, recordErr: fmt.Errorf("lockfile save: disk full")}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	_, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        false,
	})
	require.NoError(t, err, "a lockfile-save failure for the retraction re-check must never fail the sync itself")

	require.Len(t, puller.recorded, 1, "RecordRetraction must still be called")
	assert.Contains(t, warnings.String(), "disk full",
		"a failure to PERSIST the retraction verdict must be surfaced, not silently swallowed")
}

// TestSyncDependencies_SkipCanonicalizesRef pins ref canonicalization in the
// installed-probe: profile refs carry version constraints and fragment
// selectors, but lockfile keys are canonical (version- and selector-less). A
// constrained ref to an installed bundle must be recognized as installed, not
// re-pulled on every sync.
func TestSyncDependencies_SkipCanonicalizesRef(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools@^1.0"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockPuller{}
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped item, got %d (pull calls: %d)", len(result.Skipped), len(puller.pullCalls))
	}
}

// TestSyncDependencies_ForceRedownload verifies Force=true behavior.
// This overrides the skip-existing logic and re-pulls all remote items.
// Useful for getting latest versions or fixing corrupted local copies.
func TestSyncDependencies_ForceRedownload(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockPuller{}
	// The bundle reads as installed; Force must re-pull it anyway.
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/ctxloom@bundles/go-tools": true}}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:           fs,
		Registry:     registry,
		Puller:       puller,
		BundleReader: reader,
		Force:        true, // Force re-download
	})
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}

	// Should have pulled (force override)
	if len(puller.pullCalls) != 1 {
		t.Errorf("expected 1 pull call with force, got %d", len(puller.pullCalls))
	}

	if result.Installed != 1 {
		t.Errorf("expected 1 installed, got %d", result.Installed)
	}
}

func TestSyncDependencies_PullError(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	puller := &syncMockPuller{
		err: fmt.Errorf("network error"),
	}

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:       fs,
		Registry: registry,
		Puller:   puller,
	})

	// Should return error status in result, not fail entirely
	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}
	if result.Errors != 1 {
		t.Errorf("expected 1 error, got %d", result.Errors)
	}
	if result.Status != "completed_with_errors" {
		t.Errorf("expected 'completed_with_errors' status, got %q", result.Status)
	}
}

type overwritePuller struct{}

func (p *overwritePuller) Pull(ctx context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
	return &remote.PullResult{
		LocalPath:   opts.LocalDir + "/bundles/test/bundle.yaml",
		SHA:         "abc1234",
		Overwritten: true, // Mark as updated
	}, nil
}

func TestSyncDependencies_UpdatedStatus(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))

	result, err := SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS:       fs,
		Registry: registry,
		Puller:   &overwritePuller{},
		Force:    true, // Force to trigger pull
	})

	if err != nil {
		t.Fatalf("SyncDependencies failed: %v", err)
	}
	if result.Updated != 1 {
		t.Errorf("expected 1 updated, got %d", result.Updated)
	}
	if result.Status != "completed" {
		t.Errorf("expected 'completed' status, got %q", result.Status)
	}
}

// ==========================================================================
// CheckMissingDependencies tests
// ==========================================================================
//
// CheckMissingDependencies is a read-only operation that identifies what's
// missing without downloading anything. Useful for status reporting.

// fakeBundleSource is a test BundleByteSource whose accessibility is driven by
// a fixed set of "readable" bundle names. A bundle is installed only when its
// content reads back without error, mirroring the production rule that a
// lockfile entry alone is not enough — the content must be retrievable at the
// locked address.
type fakeBundleSource struct {
	readable map[string]bool
}

func (f fakeBundleSource) ReadBundleBytes(_ context.Context, name string) ([]byte, error) {
	if f.readable[name] {
		return []byte("version: 1"), nil
	}
	return nil, fmt.Errorf("%w: %s", remote.ErrBundleNotInLockfile, name)
}

func (f fakeBundleSource) LockEntryFor(string) (remote.LockEntry, bool) {
	return remote.LockEntry{}, false
}

func (f fakeBundleSource) ListBundleNames() []string { return nil }

func (f fakeBundleSource) HasBundle(name string) bool { return f.readable[name] }

// TestCheckMissingDependencies verifies detection of missing vs installed bundles.
func TestCheckMissingDependencies(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"https://github.com/test/forge@bundles/go-tools", // Missing
					"https://github.com/test/forge@bundles/security", // Installed
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	// One bundle's content is retrievable at its locked address, the other's
	// is not.
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/forge@bundles/security": true}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		BundleReader: reader,
	})
	if err != nil {
		t.Fatalf("CheckMissingDependencies failed: %v", err)
	}

	if result.Status != "missing" {
		t.Errorf("expected status 'missing', got %q", result.Status)
	}

	if result.Count != 1 {
		t.Errorf("expected 1 missing, got %d", result.Count)
	}

	if len(result.Missing) != 1 || result.Missing[0].Reference != "https://github.com/test/forge@bundles/go-tools" {
		t.Errorf("expected missing go-tools, got %v", result.Missing)
	}
}

// TestCheckMissingDependencies_RetiredProfileRefNotOffered pins that a retired
// top-level @profiles/ ref in profiles.defaults is NOT offered as an installable
// dependency. It carries no selector, cannot be installed as a bundle, and the
// actual sync (addRemoteBundleBase) skips it — so offering it here prompts the
// user y/N for a dependency that is then immediately rejected.
func TestCheckMissingDependencies_RetiredProfileRefNotOffered(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no host ~/.ctxloom leak into the defaults path
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		DefaultAgent: "default",
		Agents:       map[string]agents.Agent{"default": {Profiles: []string{"https://github.com/o/r@profiles/dev"}}},
		Profiles: config.ProfilesConfig{
			Definitions: map[string]config.Profile{},
		},
		AppPaths: []string{testBaseDir},
	})
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		BundleReader: fakeBundleSource{readable: map[string]bool{}},
	})
	if err != nil {
		t.Fatalf("CheckMissingDependencies failed: %v", err)
	}
	if result.Count != 0 {
		t.Errorf("retired @profiles/ default must not be offered; got %d missing: %v", result.Count, result.Missing)
	}
}

func TestCheckMissingDependencies_AllInstalled(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/forge@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/forge@bundles/go-tools": true}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		BundleReader: reader,
	})
	if err != nil {
		t.Fatalf("CheckMissingDependencies failed: %v", err)
	}

	if result.Status != "complete" {
		t.Errorf("expected status 'complete', got %q", result.Status)
	}

	if result.Count != 0 {
		t.Errorf("expected 0 missing, got %d", result.Count)
	}
}

// TestCheckMissingDependencies_BundleProfileParentProbedAsBundle pins the
// post-retirement rule: a bundle-profile parent (<url>@bundles/x#profiles/y) is
// installed exactly when its underlying bundle is retrievable from the clone
// cache. Top-level @profiles/ distribution is gone; parents resolve through the
// bundle they ship in.
func TestCheckMissingDependencies_BundleProfileParentProbedAsBundle(t *testing.T) {
	parent := "https://github.com/test/forge@bundles/kit#profiles/dev"
	bundleKey := "https://github.com/test/forge@bundles/kit"
	cfgFor := func() *config.Config {
		return config.NewFixture(config.Fixture{
			Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
				"test": {Parents: []string{parent}},
			}},
			AppPaths: []string{testBaseDir},
		})
	}

	t.Run("retrievable parent bundle is installed", func(t *testing.T) {
		result, err := CheckMissingDependencies(context.Background(), cfgFor(), CheckMissingDependenciesRequest{
			BundleReader: fakeBundleSource{readable: map[string]bool{bundleKey: true}},
		})
		if err != nil {
			t.Fatalf("CheckMissingDependencies failed: %v", err)
		}
		if result.Count != 0 {
			t.Errorf("expected 0 missing (parent bundle retrievable), got %d: %v", result.Count, result.Missing)
		}
	})

	t.Run("unretrievable parent bundle is missing", func(t *testing.T) {
		result, err := CheckMissingDependencies(context.Background(), cfgFor(), CheckMissingDependenciesRequest{
			BundleReader: fakeBundleSource{},
		})
		if err != nil {
			t.Fatalf("CheckMissingDependencies failed: %v", err)
		}
		if result.Count != 1 {
			t.Errorf("expected 1 missing, got %d", result.Count)
		}
	})
}

// TestCheckMissingDependencies_CanonicalizesRefs pins ref canonicalization in
// the installed-probe: lockfile keys are canonical refs (no version constraint,
// no fragment selector), so refs carrying either must still match their entry.
func TestCheckMissingDependencies_CanonicalizesRefs(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"https://github.com/test/forge@bundles/go-tools@^1.0",
					"https://github.com/test/forge@bundles/security#fragments/conduct",
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	reader := fakeBundleSource{readable: map[string]bool{
		"https://github.com/test/forge@bundles/go-tools": true,
		"https://github.com/test/forge@bundles/security": true,
	}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		BundleReader: reader,
	})
	if err != nil {
		t.Fatalf("CheckMissingDependencies failed: %v", err)
	}

	if result.Count != 0 {
		t.Errorf("expected 0 missing, got %d: %v", result.Count, result.Missing)
	}
}

// TestCheckMissingDependencies_DanglingLockEntry is the regression guard for the
// re-prompt-on-every-startup bug: a bundle recorded in the lockfile but whose
// content is not retrievable at the locked address must report as missing, not
// installed. The fake source returns an error for the bundle name, standing in
// for a SHA that is absent from the clone cache.
func TestCheckMissingDependencies_DanglingLockEntry(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/forge@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	// Empty readable set: the entry exists conceptually but content cannot be
	// read back.
	reader := fakeBundleSource{readable: map[string]bool{}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		BundleReader: reader,
	})
	if err != nil {
		t.Fatalf("CheckMissingDependencies failed: %v", err)
	}

	if result.Status != "missing" {
		t.Errorf("expected status 'missing', got %q", result.Status)
	}
	if result.Count != 1 || len(result.Missing) != 1 || result.Missing[0].Reference != "https://github.com/test/forge@bundles/go-tools" {
		t.Errorf("expected go-tools missing, got %v", result.Missing)
	}
}

// ==========================================================================
// SyncOnStartup tests
// ==========================================================================
//
// SyncOnStartup is called during `ctxloom run` to ensure dependencies are present
// before starting the LLM session. It's the automatic sync mechanism.

// TestSyncOnStartup verifies that startup sync works with local-only config.
// When no remote dependencies exist, status is "up_to_date" immediately.
func TestSyncOnStartup(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"local-only"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	// Create profiles directory
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	// With only local bundles, should return up_to_date or empty
	result, err := SyncOnStartup(context.Background(), cfg)
	if err != nil {
		t.Fatalf("SyncOnStartup failed: %v", err)
	}

	// Should be up_to_date since no remote dependencies
	if result.Status != "up_to_date" {
		t.Errorf("expected status 'up_to_date', got %q", result.Status)
	}
}

func TestSyncOnStartup_WithMissingDependencies(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	})

	// Create necessary directories
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755)

	_ = afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
    version: v1
`), 0644)

	// SyncOnStartup would call SyncDependencies, which would fail without proper mocking
	// This test verifies the flow reaches SyncDependencies
	result, err := SyncOnStartup(context.Background(), cfg)

	// Should either succeed or return error from sync (expected due to missing mocks)
	if err == nil && result != nil {
		// If successful, should be "completed" or similar
		t.Logf("SyncOnStartup result status: %s", result.Status)
	}
}

// TestCollectProfileReferences_ConfigProfile tests collecting refs from config-based profile.
func TestCollectProfileReferences_ConfigProfile(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"dev": {
				Bundles: []string{"golang", "python"},
				Parents: []string{"base-config"},
			},
		}},
	})

	bundles, profiles := collectProfileReferences(cfg, "dev")
	if len(bundles) != 2 || bundles[0] != "golang" || bundles[1] != "python" {
		t.Errorf("got bundles %v, want [golang python]", bundles)
	}
	if len(profiles) != 1 || profiles[0] != "base-config" {
		t.Errorf("got profiles %v, want [base-config]", profiles)
	}
}

func TestCollectProfileReferences_NotFound(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	})
	// No profile loader configured

	bundles, profiles := collectProfileReferences(cfg, "nonexistent")
	if len(bundles) != 0 || len(profiles) != 0 {
		t.Errorf("expected empty slices for nonexistent profile, got bundles=%v profiles=%v", bundles, profiles)
	}
}

func TestCollectProfileReferences_DirectoryProfile(t *testing.T) {
	// This test verifies the code path where a profile is loaded from the directory
	// when it's not found in cfg.GetProfilesConfig()
	//
	// Note: Testing the full directory path requires OS filesystem or mocking
	// the profiles.GetProfileDirs function, which uses os.Stat directly.
	// For now, we test that the fallback path exists by creating a profile
	// that will be found in a real directory, or by verifying the error path.

	cfg := config.NewFixture(config.Fixture{
		AppPaths: []string{"/nonexistent"},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	})

	// This should call GetProfileLoader and try to load from directory
	// Since the directory doesn't exist, it will return empty slices
	bundles, profiles := collectProfileReferences(cfg, "dev")

	// Verify the function returns empty slices when profile not found
	assert.Nil(t, bundles)
	assert.Nil(t, profiles)
}

// TestAddSyncItem_InstalledStatus tests adding an installed item.
func TestAddSyncItem_InstalledStatus(t *testing.T) {
	result := &SyncDependenciesResult{}
	item := SyncItem{
		Reference: "test-bundle",
		Type:      "bundle",
		Status:    "installed",
		LocalPath: "/path/to/bundle",
	}

	addSyncItem(result, item)

	if result.Installed != 1 {
		t.Errorf("expected Installed=1, got %d", result.Installed)
	}
	if len(result.Synced) != 1 {
		t.Errorf("expected 1 synced item, got %d", len(result.Synced))
	}
}

func TestAddSyncItem_UpdatedStatus(t *testing.T) {
	result := &SyncDependenciesResult{}
	item := SyncItem{
		Reference: "test-bundle",
		Type:      "bundle",
		Status:    "updated",
		LocalPath: "/path/to/bundle",
	}

	addSyncItem(result, item)

	if result.Updated != 1 {
		t.Errorf("expected Updated=1, got %d", result.Updated)
	}
	if len(result.Synced) != 1 {
		t.Errorf("expected 1 synced item, got %d", len(result.Synced))
	}
}

func TestAddSyncItem_SkippedStatus(t *testing.T) {
	result := &SyncDependenciesResult{}
	item := SyncItem{
		Reference: "test-bundle",
		Type:      "bundle",
		Status:    "skipped",
	}

	addSyncItem(result, item)

	if len(result.Skipped) != 1 {
		t.Errorf("expected 1 skipped item, got %d", len(result.Skipped))
	}
}

func TestAddSyncItem_FailedStatus(t *testing.T) {
	result := &SyncDependenciesResult{}
	item := SyncItem{
		Reference: "test-bundle",
		Type:      "bundle",
		Status:    "failed",
		Error:     "network error",
	}

	addSyncItem(result, item)

	if result.Errors != 1 {
		t.Errorf("expected Errors=1, got %d", result.Errors)
	}
	if len(result.Failed) != 1 {
		t.Errorf("expected 1 failed item, got %d", len(result.Failed))
	}
}

// addSyncItem's switch had no default arm, so an item with an
// unrecognized Status silently vanished from every result bucket and
// counter — result.Total (bumped by the caller for every item) and the sum
// of the buckets would then disagree with no diagnostic at all.
func TestAddSyncItem_UnknownStatusIsNotSilentlyDropped(t *testing.T) {
	result := &SyncDependenciesResult{}
	item := SyncItem{
		Reference: "test-bundle",
		Type:      "bundle",
		Status:    "not-a-real-status",
	}

	addSyncItem(result, item)

	total := len(result.Synced) + len(result.Skipped) + len(result.Retracted) + len(result.Failed)
	assert.Equal(t, 1, total, "an item with an unrecognized status must land in exactly one bucket, not vanish from all of them")
	assert.Equal(t, 1, result.Errors)
}

func TestCollectProfileReferencesRecursive_NestedLocalProfiles(t *testing.T) {
	// Test that remote dependencies in nested local profile parents are discovered
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			// Top-level profile with local parent
			"driftway": {
				Bundles: []string{"local-bundle"},
				Parents: []string{"profile:personal/typescript-dev"},
			},
			// Local parent profile with remote parent
			"personal/typescript-dev": {
				Bundles: []string{"https://github.com/owner/repo@v1/bundles/core"},
				Parents: []string{"https://github.com/owner/repo@v1/bundles/base-kit#profiles/base"},
			},
		}},
	})

	bundleSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	collectProfileReferencesRecursive(cfg, "driftway", bundleSet, visited)

	// Should find the remote bundle from the nested local parent
	assert.True(t, bundleSet.Has("https://github.com/owner/repo@v1/bundles/core"),
		"should find remote bundle in nested local parent")

	// A bundle-profile parent contributes its underlying bundle (the #profiles/
	// selector is stripped), so the whole nested remote closure is bundles now.
	assert.True(t, bundleSet.Has("https://github.com/owner/repo@v1/bundles/base-kit"),
		"should find the bundle behind a nested bundle-profile parent")

	// Should NOT include local-bundle (it's not a remote reference)
	assert.False(t, bundleSet.Has("local-bundle"),
		"should not include local bundles")
}

func TestCollectProfileReferencesRecursive_ProfilePrefixStripped(t *testing.T) {
	// Test that "profile:" prefix is properly stripped when following local parents
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"top": {
				Parents: []string{"profile:nested/profile"},
			},
			"nested/profile": {
				Bundles: []string{"https://github.com/test/forge@bundles/remote-bundle"},
			},
		}},
	})

	bundleSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	collectProfileReferencesRecursive(cfg, "top", bundleSet, visited)

	// Should find the remote bundle from nested/profile
	assert.True(t, bundleSet.Has("https://github.com/test/forge@bundles/remote-bundle"),
		"should find remote bundle after stripping profile: prefix")
}

func TestCollectProfileReferencesRecursive_CircularDependency(t *testing.T) {
	// Test that circular dependencies don't cause infinite loops
	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"profile-a": {
				Parents: []string{"profile:profile-b"},
			},
			"profile-b": {
				Parents: []string{"profile:profile-a"},
				Bundles: []string{"https://github.com/test/forge@bundles/bundle"},
			},
		}},
	})

	bundleSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	// Should not panic or infinite loop
	collectProfileReferencesRecursive(cfg, "profile-a", bundleSet, visited)

	// Should still find the bundle
	assert.True(t, bundleSet.Has("https://github.com/test/forge@bundles/bundle"))
}

// TestRunSyncPostSteps_Guards pins the two guard conditions in runSyncPostSteps
// (sync.go:160/168) via recording seams: the lockfile step fires only when
// req.Lock AND there was at least one install/update; the hooks step fires only
// when req.ApplyHooks AND there was at least one remote reference (Total > 0).
// The table covers each flag off, each boundary at zero, and that Installed and
// Updated each independently satisfy the lock guard.
func TestRunSyncPostSteps_Guards(t *testing.T) {
	origLock, origHooks := syncLockStep, syncHooksStep
	t.Cleanup(func() { syncLockStep, syncHooksStep = origLock, origHooks })

	var lockCalls, hookCalls int
	syncLockStep = func(context.Context, *config.Config, LockDependenciesRequest) (*LockDependenciesResult, error) {
		lockCalls++
		return &LockDependenciesResult{}, nil
	}
	syncHooksStep = func(context.Context, ApplyHooksRequest) (*ApplyHooksResult, error) {
		hookCalls++
		return &ApplyHooksResult{}, nil
	}

	tests := []struct {
		name                      string
		lock, applyHooks          bool
		installed, updated, total int
		wantLock, wantHooks       bool
	}{
		{name: "all_off", total: 1},
		{name: "lock_off_with_installs", installed: 1, total: 1},
		{name: "lock_on_no_changes", lock: true, total: 1, wantLock: false},
		{name: "lock_on_installed", lock: true, installed: 1, total: 1, wantLock: true},
		{name: "lock_on_updated_only", lock: true, updated: 1, total: 1, wantLock: true},
		{name: "hooks_off_with_total", total: 1},
		{name: "hooks_on_zero_total", applyHooks: true, total: 0, wantHooks: false},
		{name: "hooks_on_with_total", applyHooks: true, total: 1, wantHooks: true},
		{name: "both_on_full", lock: true, applyHooks: true, installed: 2, total: 2, wantLock: true, wantHooks: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lockCalls, hookCalls = 0, 0
			result := &SyncDependenciesResult{Installed: tt.installed, Updated: tt.updated, Total: tt.total}
			req := SyncDependenciesRequest{Lock: tt.lock, ApplyHooks: tt.applyHooks}

			runSyncPostSteps(context.Background(), &config.Config{}, req, result, afero.NewMemMapFs())

			assert.Equal(t, tt.wantLock, lockCalls == 1, "lockfile step fired=%v, want %v", lockCalls == 1, tt.wantLock)
			assert.Equal(t, tt.wantHooks, hookCalls == 1, "hooks step fired=%v, want %v", hookCalls == 1, tt.wantHooks)
		})
	}
}

// revealingPuller simulates the real cascade that made `remote pull` need
// repeated invocation: a profile that cannot load until one of its remote
// dependencies is installed contributes NO references to the first collect
// pass. Pulling that dependency makes the profile loadable, revealing bundle
// refs the initial pass never saw. Here, pulling `reveals` writes the
// previously-unloadable profile into the profiles dir.
type revealingPuller struct {
	cfg         *config.Config
	fs          afero.Fs // re-injected when cfg is rebuilt; a Fixture carries no fs
	reveals     string   // pulling this ref makes the revealed profile resolvable
	revealedRef string   // the bundle that newly-resolvable profile references
	pulled      []string
}

func (p *revealingPuller) Pull(_ context.Context, refStr string, _ remote.PullOptions) (*remote.PullResult, error) {
	p.pulled = append(p.pulled, refStr)
	if refStr == p.reveals {
		// Every read of a Config's profiles is copy-on-read — ToFixture
		// included — so making a definition appear mid-run
		// means rebuilding the config the sync loop is holding. Assigning into
		// a returned map would be exactly the silent no-op this test exists to
		// catch, just relocated into the test double.
		installProfileDefs(p.cfg, p.fs, map[string]config.Profile{
			"revealed": {Bundles: []string{p.revealedRef}},
		})
	}
	return &remote.PullResult{LocalPath: paths.CacheBundlesPath(testBaseDir) + "/revealed.yaml"}, nil
}

// TestSyncDependencies_PullsRefsRevealedByEarlierPulls pins the fixed-point
// contract: references that only become discoverable *because* of an earlier
// pull in the same run must still be pulled. Without it, sync exits 0 having
// silently left part of the dependency graph unpinned, and the user must run
// `remote pull` again (and again) to converge.
func TestSyncDependencies_PullsRefsRevealedByEarlierPulls(t *testing.T) {
	fs := afero.NewMemMapFs()

	const (
		rootRef     = "https://github.com/test/ctxloom@bundles/alpha"
		revealedRef = "https://github.com/test/ctxloom@bundles/beta"
	)

	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755))
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
`), 0644))

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"root": {Bundles: []string{rootRef}},
		}},
		AppPaths: []string{testBaseDir},
	})
	cfg.SetFS(fs)

	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	puller := &revealingPuller{cfg: cfg, fs: fs, reveals: rootRef, revealedRef: revealedRef}

	_, err = SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS: fs, Registry: registry, Puller: puller,
	})
	require.NoError(t, err)

	assert.Contains(t, puller.pulled, rootRef, "the directly-referenced bundle must be pulled")
	assert.Contains(t, puller.pulled, revealedRef,
		"a bundle revealed by an earlier pull must also be pulled in the same run; "+
			"sync must iterate collect->pull to a fixed point rather than collecting refs once")
}

// chainRevealingPuller reveals exactly one further bundle ref per pull,
// forming a chain that takes one sync pass per link. With a chain long enough
// to occupy every allowed pass, the LAST pass still does real work but the
// re-collect after it reveals nothing — the graph converged on the final
// permitted pass.
type chainRevealingPuller struct {
	cfg    *config.Config
	fs     afero.Fs          // re-injected when cfg is rebuilt; a Fixture carries no fs
	next   map[string]string // ref -> the ref pulling it reveals ("" = reveals nothing)
	pulled []string
}

func (p *chainRevealingPuller) Pull(_ context.Context, refStr string, _ remote.PullOptions) (*remote.PullResult, error) {
	p.pulled = append(p.pulled, refStr)
	if revealed := p.next[refStr]; revealed != "" {
		installProfileDefs(p.cfg, p.fs, map[string]config.Profile{
			revealed: {Bundles: []string{revealed}},
		})
	}
	return &remote.PullResult{LocalPath: paths.CacheBundlesPath(testBaseDir) + "/chain.yaml"}, nil
}

// TestSyncDependencies_NoUnconvergedWarningWhenLastPassConverges:
// the "still revealing new references" warning must describe the GRAPH, not
// the loop counter. A chain that needs every allowed pass and then converges
// on the last one is a fully-converged sync — warning there tells the user to
// re-run `remote pull` for nothing, and the re-run finds no work.
func TestSyncDependencies_NoUnconvergedWarningWhenLastPassConverges(t *testing.T) {
	fs := afero.NewMemMapFs()

	require.NoError(t, fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755))
	require.NoError(t, fs.MkdirAll(paths.LocalBundlesPath(testBaseDir), 0755))
	require.NoError(t, afero.WriteFile(fs, paths.RemotesPath(testBaseDir), []byte(`
remotes:
  github:
    url: https://github.com/test/ctxloom
`), 0644))

	// One link per allowed pass: pass i pulls link i and reveals link i+1,
	// except the final pull, which reveals nothing.
	refs := make([]string, maxSyncPasses)
	next := map[string]string{}
	for i := range refs {
		refs[i] = fmt.Sprintf("https://github.com/test/ctxloom@bundles/link%d", i)
	}
	for i := 0; i < len(refs)-1; i++ {
		next[refs[i]] = refs[i+1]
	}

	cfg := config.NewFixture(config.Fixture{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			refs[0]: {Bundles: []string{refs[0]}},
		}},
		AppPaths: []string{testBaseDir},
	})
	cfg.SetFS(fs)

	registry, err := remote.NewRegistry(paths.RemotesPath(testBaseDir), remote.WithRegistryFS(fs))
	require.NoError(t, err)

	puller := &chainRevealingPuller{cfg: cfg, fs: fs, next: next}

	var warnings bytes.Buffer
	restore := clidiag.SetSink(&warnings)
	defer restore()

	_, err = SyncDependencies(context.Background(), cfg, SyncDependenciesRequest{
		FS: fs, Registry: registry, Puller: puller,
	})
	require.NoError(t, err)

	require.Len(t, puller.pulled, len(refs), "every link in the chain must be pulled")
	assert.NotContains(t, warnings.String(), "still revealing new references",
		"the graph converged on the final allowed pass; warning here sends the user on a no-op re-run")
}
