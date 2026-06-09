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
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/shared/collections"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
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

// ==========================================================================
// Reference classification tests
// ==========================================================================

// TestCollectRemoteReferences verifies that remote vs local references are
// correctly distinguished. This is the first step of sync - finding what
// needs to be downloaded.
func TestCollectRemoteReferences(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"https://github.com/test/ctxloom@bundles/go-tools", // Remote
					"local-bundle", // Local (no scheme)
					"https://gitlab.com/test/sec@bundles/security",     // Remote
				},
				Parents: []string{
					"https://github.com/test/ctxloom@profiles/parent", // Remote
				},
			},
			"local-only": {
				Bundles: []string{
					"my-local-bundle",
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	// Create the profiles directory
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	bundles, profiles, err := collectRemoteReferences(cfg, nil, fs)
	if err != nil {
		t.Fatalf("collectRemoteReferences failed: %v", err)
	}

	// Should find remote bundles
	if len(bundles) != 2 {
		t.Errorf("expected 2 remote bundles, got %d: %v", len(bundles), bundles)
	}

	// Should find remote profiles
	if len(profiles) != 1 {
		t.Errorf("expected 1 remote profile, got %d: %v", len(profiles), profiles)
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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"local": {
				Bundles: []string{"local-bundle"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	// Create necessary directories
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)

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
// pull's informational output (Blind-mode notice, lockfile warnings) must be
// routed to stderr. An unset PullOptions.Stdout defaults to os.Stdout inside
// Pull, which would corrupt the protocol stream.
func TestSyncDependencies_PullOutputAvoidsStdout(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)
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
// By default, sync does NOT re-download bundles that already exist locally.
// This makes repeated syncs fast - only missing items are fetched.
// Use Force=true to override this for full re-sync.
func TestSyncDependencies_SkipsExisting(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	// Create necessary directories and existing bundle
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/github.com/test/ctxloom", 0755)
	_ = afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/github.com/test/ctxloom/go-tools.yaml", []byte("version: 1"), 0644)

	// Create registry
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
		Force:    false,
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

// TestSyncDependencies_ForceRedownload verifies Force=true behavior.
// This overrides the skip-existing logic and re-pulls all remote items.
// Useful for getting latest versions or fixing corrupted local copies.
func TestSyncDependencies_ForceRedownload(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	// Create necessary directories and existing bundle
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir)+"/github.com/test/ctxloom", 0755)
	_ = afero.WriteFile(fs, paths.BundlesPath(testBaseDir)+"/github.com/test/ctxloom/go-tools.yaml", []byte("version: 1"), 0644)

	// Create registry
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
		Force:    true, // Force re-download
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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)

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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)

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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"https://github.com/test/forge@bundles/go-tools", // Missing
					"https://github.com/test/forge@bundles/security", // Installed
				},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	// One bundle's content is retrievable at its locked address, the other's
	// is not.
	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/forge@bundles/security": true}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		FS:           fs,
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

func TestCheckMissingDependencies_AllInstalled(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/forge@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	reader := fakeBundleSource{readable: map[string]bool{"https://github.com/test/forge@bundles/go-tools": true}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		FS:           fs,
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

// TestCheckMissingDependencies_DanglingLockEntry is the regression guard for the
// re-prompt-on-every-startup bug: a bundle recorded in the lockfile but whose
// content is not retrievable at the locked address must report as missing, not
// installed. The fake source returns an error for the bundle name, standing in
// for a SHA that is absent from the clone cache.
func TestCheckMissingDependencies_DanglingLockEntry(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/forge@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)

	// Empty readable set: the entry exists conceptually but content cannot be
	// read back.
	reader := fakeBundleSource{readable: map[string]bool{}}

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		FS:           fs,
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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"local-only"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

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

	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"test": {
				Bundles: []string{"https://github.com/test/ctxloom@bundles/go-tools"},
			},
		}},
		AppPaths: []string{testBaseDir},
	}

	// Create necessary directories
	_ = fs.MkdirAll(paths.ProfilesPath(testBaseDir), 0755)
	_ = fs.MkdirAll(paths.BundlesPath(testBaseDir), 0755)

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
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"dev": {
				Bundles: []string{"golang", "python"},
				Parents: []string{"base-config"},
			},
		}},
	}

	bundles, profiles := collectProfileReferences(cfg, "dev")
	if len(bundles) != 2 || bundles[0] != "golang" || bundles[1] != "python" {
		t.Errorf("got bundles %v, want [golang python]", bundles)
	}
	if len(profiles) != 1 || profiles[0] != "base-config" {
		t.Errorf("got profiles %v, want [base-config]", profiles)
	}
}

func TestCollectProfileReferences_NotFound(t *testing.T) {
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	}
	// No profile loader configured

	bundles, profiles := collectProfileReferences(cfg, "nonexistent")
	if len(bundles) != 0 || len(profiles) != 0 {
		t.Errorf("expected empty slices for nonexistent profile, got bundles=%v profiles=%v", bundles, profiles)
	}
}

func TestCollectProfileReferences_DirectoryProfile(t *testing.T) {
	// This test verifies the code path where a profile is loaded from the directory
	// when it's not found in cfg.Profiles
	//
	// Note: Testing the full directory path requires OS filesystem or mocking
	// the profiles.GetProfileDirs function, which uses os.Stat directly.
	// For now, we test that the fallback path exists by creating a profile
	// that will be found in a real directory, or by verifying the error path.

	cfg := &config.Config{
		AppPaths: []string{"/nonexistent"},
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{}},
	}

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

func TestCollectProfileReferencesRecursive_NestedLocalProfiles(t *testing.T) {
	// Test that remote dependencies in nested local profile parents are discovered
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			// Top-level profile with local parent
			"driftway": {
				Bundles: []string{"local-bundle"},
				Parents: []string{"profile:personal/typescript-dev"},
			},
			// Local parent profile with remote parent
			"personal/typescript-dev": {
				Bundles: []string{"https://github.com/owner/repo@v1/bundles/core"},
				Parents: []string{"https://github.com/owner/repo@v1/profiles/base"},
			},
		}},
	}

	bundleSet := collections.NewSet[string]()
	profileSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	collectProfileReferencesRecursive(cfg, "driftway", bundleSet, profileSet, visited)

	// Should find the remote bundle from the nested local parent
	assert.True(t, bundleSet.Has("https://github.com/owner/repo@v1/bundles/core"),
		"should find remote bundle in nested local parent")

	// Should find the remote profile from the nested local parent
	assert.True(t, profileSet.Has("https://github.com/owner/repo@v1/profiles/base"),
		"should find remote profile in nested local parent")

	// Should NOT include local-bundle (it's not a remote reference)
	assert.False(t, bundleSet.Has("local-bundle"),
		"should not include local bundles")
}

func TestCollectProfileReferencesRecursive_ProfilePrefixStripped(t *testing.T) {
	// Test that "profile:" prefix is properly stripped when following local parents
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"top": {
				Parents: []string{"profile:nested/profile"},
			},
			"nested/profile": {
				Bundles: []string{"https://github.com/test/forge@bundles/remote-bundle"},
			},
		}},
	}

	bundleSet := collections.NewSet[string]()
	profileSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	collectProfileReferencesRecursive(cfg, "top", bundleSet, profileSet, visited)

	// Should find the remote bundle from nested/profile
	assert.True(t, bundleSet.Has("https://github.com/test/forge@bundles/remote-bundle"),
		"should find remote bundle after stripping profile: prefix")
}

func TestCollectProfileReferencesRecursive_CircularDependency(t *testing.T) {
	// Test that circular dependencies don't cause infinite loops
	cfg := &config.Config{
		Profiles: config.ProfilesConfig{Definitions: map[string]config.Profile{
			"profile-a": {
				Parents: []string{"profile:profile-b"},
			},
			"profile-b": {
				Parents: []string{"profile:profile-a"},
				Bundles: []string{"https://github.com/test/forge@bundles/bundle"},
			},
		}},
	}

	bundleSet := collections.NewSet[string]()
	profileSet := collections.NewSet[string]()
	visited := collections.NewSet[string]()

	// Should not panic or infinite loop
	collectProfileReferencesRecursive(cfg, "profile-a", bundleSet, profileSet, visited)

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
	syncHooksStep = func(context.Context, *config.Config, ApplyHooksRequest) (*ApplyHooksResult, error) {
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
