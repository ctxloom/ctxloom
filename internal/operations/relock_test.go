package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// relockCountingUpdater records UpdateRepo calls per URL so the relock dedup
// pre-pass invariant is observable.
type relockCountingUpdater struct {
	calls map[string]int
}

func (c *relockCountingUpdater) UpdateRepo(ctx context.Context, repoURL string, forgeType remote.ForgeType) (string, error) {
	if c.calls == nil {
		c.calls = map[string]int{}
	}
	c.calls[repoURL]++
	return "", nil
}

// relockTestProject sets up a temp project with one registered remote and a
// profile referencing two bundles from it (plus a malformed ref). Returns the
// baseDir, the registry, and the cfg.
func relockTestProject(t *testing.T, repoURL string) (string, *remote.Registry, *config.Config) {
	t.Helper()
	baseDir := t.TempDir()

	reg, err := remote.NewRegistry(filepath.Join(baseDir, "remotes"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Add("alice", repoURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	profilesDir := filepath.Join(baseDir, "profiles", "alice")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	profileYAML := "name: dev\nbundles:\n  - alice/sec\n  - alice/two\n  - malformed\n"
	if err := os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte(profileYAML), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	cfg := &config.Config{
		AppPaths: []string{baseDir},
		Defaults: config.Defaults{Profiles: []string{"alice/dev"}},
	}
	return baseDir, reg, cfg
}

// TestRelock_WritesResolvedEntriesWithoutExistingLockfile is the core contract:
// Relock rebuilds lock.yaml from the configured profiles even when NO lockfile
// exists, dedups the per-URL refresh, and pins each resolved entry at the
// fetched SHA.
func TestRelock_WritesResolvedEntriesWithoutExistingLockfile(t *testing.T) {
	ctx := context.Background()
	repoURL := "https://github.com/alice/ctxloom"
	baseDir, reg, cfg := relockTestProject(t, repoURL)

	// Precondition: no lockfile exists yet — the scenario relock must handle.
	lm := remote.NewLockfileManager(baseDir)
	if lf, _ := lm.Load(); lf != nil && !lf.IsEmpty() {
		t.Fatal("precondition: expected no pre-existing lockfile entries")
	}

	const wantSHA = "deadbeefcafe1234567890123456789012345678"
	counting := &relockCountingUpdater{}
	resolving := FetcherFactory(func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return remote.NewMockFetcher().WithRef("main", wantSHA), nil
	})

	result, err := Relock(ctx, cfg, RelockRequest{
		Registry:       reg,
		RepoCache:      counting,
		FetcherFactory: resolving,
	})
	if err != nil {
		t.Fatalf("Relock: %v", err)
	}

	if result.Status != "regenerated" {
		t.Fatalf("status = %q (msg=%q errs=%v), want regenerated", result.Status, result.Message, result.Errors)
	}
	// alice/sec and alice/two resolve; "malformed" (no slash) is not collected
	// as a remote reference, so it never reaches relock — exactly 2 pin.
	if result.ItemCount != 2 {
		t.Fatalf("ItemCount = %d, want 2 (errs=%v)", result.ItemCount, result.Errors)
	}
	// Dedup: one UpdateRepo per unique URL though two bundles share it.
	if got := counting.calls[repoURL]; got != 1 {
		t.Fatalf("UpdateRepo calls for shared URL = %d, want 1 (dedup)", got)
	}

	lf, err := lm.Load()
	if err != nil {
		t.Fatalf("load regenerated lockfile: %v", err)
	}
	entry, ok := lf.GetEntry(remote.ItemTypeBundle, "alice/sec")
	if !ok {
		t.Fatal("alice/sec not in regenerated lockfile")
	}
	if entry.SHA != wantSHA {
		t.Fatalf("alice/sec SHA = %q, want %q", entry.SHA, wantSHA)
	}
	if _, ok := lf.GetEntry(remote.ItemTypeBundle, "alice/two"); !ok {
		t.Fatal("alice/two not in regenerated lockfile")
	}
}

// TestRelock_PinsCanonicalURLParent is the regression for a profile parent
// written as a canonical URL (e.g. "https://…@profiles/go-developer"). Such a
// ref has no registry remote name — only a URL — so the old code's
// registry.Get(ref.Remote) returned "remote not found" and silently skipped it.
// Relock must resolve it by URL and pin it under the short "<repo>/<path>" key.
func TestRelock_PinsCanonicalURLParent(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()

	// Registry knows the bundle remote "alice" but NOT the parent's repo: the
	// canonical parent must resolve purely from its embedded URL.
	bundleURL := "https://github.com/alice/ctxloom"
	parentURL := "https://github.com/upstream/ctxloom-default"
	reg, err := remote.NewRegistry(filepath.Join(baseDir, "remotes"))
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	if err := reg.Add("alice", bundleURL); err != nil {
		t.Fatalf("add remote: %v", err)
	}

	profilesDir := filepath.Join(baseDir, "profiles", "alice")
	if err := os.MkdirAll(profilesDir, 0o755); err != nil {
		t.Fatalf("mkdir profiles: %v", err)
	}
	profileYAML := "name: dev\n" +
		"parents:\n  - " + parentURL + "@profiles/go-developer\n" +
		"bundles:\n  - alice/sec\n"
	if err := os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte(profileYAML), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	cfg := &config.Config{
		AppPaths: []string{baseDir},
		Defaults: config.Defaults{Profiles: []string{"alice/dev"}},
	}

	const wantSHA = "deadbeefcafe1234567890123456789012345678"
	resolving := FetcherFactory(func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		return remote.NewMockFetcher().WithRef("main", wantSHA), nil
	})

	result, err := Relock(ctx, cfg, RelockRequest{
		Registry:       reg,
		RepoCache:      &relockCountingUpdater{},
		FetcherFactory: resolving,
	})
	if err != nil {
		t.Fatalf("Relock: %v", err)
	}
	if result.Status != "regenerated" {
		t.Fatalf("status = %q (errs=%v), want regenerated", result.Status, result.Errors)
	}
	// One bundle + one canonical parent profile, both resolved, none skipped.
	if result.ItemCount != 2 {
		t.Fatalf("ItemCount = %d, want 2 (errs=%v)", result.ItemCount, result.Errors)
	}
	if result.Failed != 0 {
		t.Fatalf("Failed = %d, want 0 (errs=%v)", result.Failed, result.Errors)
	}

	lf, err := remote.NewLockfileManager(baseDir).Load()
	if err != nil {
		t.Fatalf("load lockfile: %v", err)
	}
	// Parent is keyed by its short "<repo>/<path>" form, not the raw URL.
	entry, ok := lf.GetEntry(remote.ItemTypeProfile, "ctxloom-default/go-developer")
	if !ok {
		t.Fatalf("canonical parent not pinned under short key; profiles=%v", lf.Profiles)
	}
	if entry.SHA != wantSHA {
		t.Fatalf("parent SHA = %q, want %q", entry.SHA, wantSHA)
	}
	if entry.URL != parentURL {
		t.Fatalf("parent URL = %q, want %q", entry.URL, parentURL)
	}
}

// TestRelock_DegradesGracefullyWhenNothingResolves verifies fault tolerance:
// when SHA resolution fails for every entry, Relock does not crash or error —
// failures are collected, status is "empty", and no lockfile is written.
func TestRelock_DegradesGracefullyWhenNothingResolves(t *testing.T) {
	ctx := context.Background()
	repoURL := "https://github.com/alice/ctxloom"
	baseDir, reg, cfg := relockTestProject(t, repoURL)

	failing := FetcherFactory(func(url string, auth remote.AuthConfig) (remote.Fetcher, error) {
		m := remote.NewMockFetcher()
		m.ResolveRefErr = context.DeadlineExceeded // every resolve fails
		return m, nil
	})

	result, err := Relock(ctx, cfg, RelockRequest{
		Registry:       reg,
		RepoCache:      &relockCountingUpdater{},
		FetcherFactory: failing,
	})
	if err != nil {
		t.Fatalf("Relock must not error on unresolved entries: %v", err)
	}
	if result.Status != "empty" {
		t.Fatalf("status = %q, want empty", result.Status)
	}
	if result.Failed != 2 {
		t.Fatalf("Failed = %d, want 2 (both bundle refs unresolved), errs=%v", result.Failed, result.Errors)
	}
	if result.ItemCount != 0 {
		t.Fatalf("ItemCount = %d, want 0", result.ItemCount)
	}
	// No lockfile written when nothing resolved.
	lm := remote.NewLockfileManager(baseDir)
	if lf, _ := lm.Load(); lf != nil && !lf.IsEmpty() {
		t.Fatal("lockfile should not be written when no entries resolved")
	}
}
