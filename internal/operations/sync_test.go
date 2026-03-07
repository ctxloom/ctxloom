// Package operations tests for sync verify remote dependency synchronization.
//
// Sync is how SCM pulls remote bundles and profiles from GitHub/GitLab/etc.
// It scans config for remote references (anything with "/" like "github/bundle")
// and downloads missing items to the local .scm directory.
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
// when `scm run` starts an LLM session to ensure context is available.
package operations

import (
	"context"
	"testing"

	"github.com/spf13/afero"

	"github.com/benjaminabbitt/scm/internal/config"
	"github.com/benjaminabbitt/scm/internal/remote"
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
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"github/go-tools",     // Remote
					"local-bundle",        // Local (no slash)
					"gitlab/security",     // Remote
				},
				Parents: []string{
					"remote/parent-profile", // Remote
				},
			},
			"local-only": {
				Bundles: []string{
					"my-local-bundle",
				},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create the profiles directory
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)

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
// full URL - SCM expands it using the remote registry.
func TestIsRemoteReference(t *testing.T) {
	tests := []struct {
		ref      string
		expected bool
	}{
		{"github/bundle", true},
		{"remote/path/bundle", true},
		{"https://github.com/owner/repo", true},
		{"git@github.com:owner/repo", true},
		{"file:///path/to/repo", true},
		{"local-bundle", false},
		{"my-bundle", false},
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
		Profiles: map[string]config.Profile{
			"local": {
				Bundles: []string{"local-bundle"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create the profiles directory
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)

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
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"github/go-tools"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create necessary directories
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)
	_ = fs.MkdirAll("/test/.scm/bundles", 0755)

	// Create registry with test remote
	_ = afero.WriteFile(fs, "/test/.scm/remotes.yaml", []byte(`
remotes:
  github:
    url: https://github.com/test/scm
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry("/test/.scm/remotes.yaml", remote.WithRegistryFS(fs))

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

// TestSyncDependencies_SkipsExisting verifies incremental sync behavior.
//
// By default, sync does NOT re-download bundles that already exist locally.
// This makes repeated syncs fast - only missing items are fetched.
// Use Force=true to override this for full re-sync.
func TestSyncDependencies_SkipsExisting(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"github/go-tools"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create necessary directories and existing bundle
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)
	_ = fs.MkdirAll("/test/.scm/bundles/github", 0755)
	_ = afero.WriteFile(fs, "/test/.scm/bundles/github/go-tools.yaml", []byte("version: 1"), 0644)

	// Create registry
	_ = afero.WriteFile(fs, "/test/.scm/remotes.yaml", []byte(`
remotes:
  github:
    url: https://github.com/test/scm
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry("/test/.scm/remotes.yaml", remote.WithRegistryFS(fs))

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
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"github/go-tools"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create necessary directories and existing bundle
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)
	_ = fs.MkdirAll("/test/.scm/bundles/github", 0755)
	_ = afero.WriteFile(fs, "/test/.scm/bundles/github/go-tools.yaml", []byte("version: 1"), 0644)

	// Create registry
	_ = afero.WriteFile(fs, "/test/.scm/remotes.yaml", []byte(`
remotes:
  github:
    url: https://github.com/test/scm
    version: v1
`), 0644)

	registry, _ := remote.NewRegistry("/test/.scm/remotes.yaml", remote.WithRegistryFS(fs))

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

// ==========================================================================
// CheckMissingDependencies tests
// ==========================================================================
//
// CheckMissingDependencies is a read-only operation that identifies what's
// missing without downloading anything. Useful for status reporting.

// TestCheckMissingDependencies verifies detection of missing vs installed bundles.
func TestCheckMissingDependencies(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{
					"github/go-tools",  // Missing
					"github/security",  // Installed
				},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create directories
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)
	_ = fs.MkdirAll("/test/.scm/bundles/github", 0755)

	// Install one bundle
	_ = afero.WriteFile(fs, "/test/.scm/bundles/github/security.yaml", []byte("version: 1"), 0644)

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		FS: fs,
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

	if len(result.Missing) != 1 || result.Missing[0].Reference != "github/go-tools" {
		t.Errorf("expected missing github/go-tools, got %v", result.Missing)
	}
}

func TestCheckMissingDependencies_AllInstalled(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"github/go-tools"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create directories and install bundle
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)
	_ = fs.MkdirAll("/test/.scm/bundles/github", 0755)
	_ = afero.WriteFile(fs, "/test/.scm/bundles/github/go-tools.yaml", []byte("version: 1"), 0644)

	result, err := CheckMissingDependencies(context.Background(), cfg, CheckMissingDependenciesRequest{
		FS: fs,
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

// ==========================================================================
// SyncOnStartup tests
// ==========================================================================
//
// SyncOnStartup is called during `scm run` to ensure dependencies are present
// before starting the LLM session. It's the automatic sync mechanism.

// TestSyncOnStartup verifies that startup sync works with local-only config.
// When no remote dependencies exist, status is "up_to_date" immediately.
func TestSyncOnStartup(t *testing.T) {
	fs := afero.NewMemMapFs()

	cfg := &config.Config{
		Profiles: map[string]config.Profile{
			"test": {
				Bundles: []string{"local-only"},
			},
		},
		SCMPaths: []string{"/test/.scm"},
	}

	// Create profiles directory
	_ = fs.MkdirAll("/test/.scm/profiles", 0755)

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
