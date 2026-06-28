package operations

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// acmeBundle is the canonical bundle-ref prefix for the (remote) acme repo, so
// seed keys parse back to RepoURL == trustRepo with bundle paths after it.
const acmeBundle = "https://github.com/acme/repo@bundles/"

// promptHash recomputes the effective-content hash for a no-distill prompt body
// (preferDistilled true ⇒ raw bytes when there is no distilled form), the value
// the baseline records for that prompt.
func promptHash(body string) string {
	p := bundles.BundlePrompt{Content: body}
	h, _ := p.EffectiveContentHash(true)
	return h
}

// mcpHashOf is the executable-surface hash the baseline records for an MCP item.
func mcpHashOf(m bundles.BundleMCP) string { return m.ComputeContentHash() }

// baselineSeed builds a loader holding one REMOTE bundle (acme tooling: a
// fragment, a prompt, an MCP server) and one LOCAL bundle (plain-named "demo":
// a fragment and an MCP server). The remote items are what the baseline should
// grant; the local items must never be granted (content auto-allows via the
// cascade, executables are never auto-trusted).
func baselineSeed() *bundles.Loader {
	seed := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "solid body"}},
			Prompts:   map[string]bundles.BundlePrompt{"review": {Content: "review body"}},
			MCP:       map[string]bundles.BundleMCP{"postgres": {Command: "pg-mcp", Args: []string{"--port", "5432"}}},
		},
		"demo": {
			Fragments: map[string]bundles.BundleFragment{"localfrag": {Content: "local body"}},
			MCP:       map[string]bundles.BundleMCP{"localmcp": {Command: "local-cmd"}},
		},
	}
	for k, b := range seed {
		b.Name = k
	}
	return bundles.NewLoader(nil, true, bundles.WithSeededBundles(seed))
}

// TestBaselineTrust_SnapshotsPresentItems pins the core TR6 contract: every
// present REMOTE bundle item is granted with the correct canonical url + ref key
// + effective-content hash; LOCAL items are skipped (never granted); and a
// baselined remote item from an UNTRUSTED remote resolves ALLOW via the grant,
// proving content present at rollout survives once enforcement lands.
func TestBaselineTrust_SnapshotsPresentItems(t *testing.T) {
	loader := baselineSeed()
	store := newTrustStore(t)

	res, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader})
	if err != nil {
		t.Fatalf("BaselineTrust: %v", err)
	}
	if res.Status != "baselined" {
		t.Errorf("status = %q, want baselined", res.Status)
	}
	if res.Granted != 3 || res.Local != 2 || res.Failed != 0 || res.Total != 5 {
		t.Errorf("tally = {granted:%d local:%d failed:%d total:%d}, want {3 2 0 5}",
			res.Granted, res.Local, res.Failed, res.Total)
	}

	// Each remote item is granted under {canonical repo, ref key, effective hash}.
	wantGrants := []struct {
		ref  string
		hash string
	}{
		{"tooling#fragments/solid", fragHash("solid body")},
		{"tooling#prompts/review", promptHash("review body")},
		{"tooling#mcp/postgres", mcpHashOf(bundles.BundleMCP{Command: "pg-mcp", Args: []string{"--port", "5432"}})},
	}
	for _, w := range wantGrants {
		if _, ok := store.GrantMatch(trustRepo, w.ref, w.hash); !ok {
			t.Errorf("missing grant for {%s, %s, %s}", trustRepo, w.ref, w.hash)
		}
	}

	// Local items are never baselined (any hash).
	if _, ok := store.GrantMatch("ctxloom:local", "demo#fragments/localfrag", fragHash("local body")); ok {
		t.Error("local fragment was granted; local content needs no grant")
	}
	localMCP := bundles.BundleMCP{Command: "local-cmd"}
	if _, ok := store.GrantMatch("ctxloom:local", "demo#mcp/localmcp", mcpHashOf(localMCP)); ok {
		t.Error("local MCP executable was granted; local executables must not be auto-trusted")
	}

	// Migration goal: an item present at rollout stays trusted even from an
	// UNTRUSTED remote — the grant short-circuits the cascade before the remote
	// tier would have denied it.
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	tref := trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "solid"}
	got, err := EffectiveTrust(nil, EffectiveTrustRequest{
		Ref: tref, ContentHash: fragHash("solid body"), Store: store, Registry: reg,
	})
	if err != nil {
		t.Fatalf("EffectiveTrust: %v", err)
	}
	if got.Decision != trust.Allow || got.Source != trust.SourceExplicitGrant {
		t.Errorf("baselined item resolve = {%s,%s}, want {allow, explicit-grant}", got.Decision, got.Source)
	}
}

// TestBaselineTrust_Idempotent proves the snapshot runs exactly once: a second
// call is a no-op that writes nothing new, both in-memory and after reloading
// the persisted store from disk.
func TestBaselineTrust_Idempotent(t *testing.T) {
	fs := afero.NewMemMapFs()
	loader := baselineSeed()
	store, err := trust.New("", trust.WithFS(fs))
	if err != nil {
		t.Fatalf("trust.New: %v", err)
	}

	if _, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader, FS: fs}); err != nil {
		t.Fatalf("BaselineTrust (first): %v", err)
	}
	after1, err := afero.ReadFile(fs, paths.DefaultTrustPath())
	if err != nil {
		t.Fatalf("read trust.yaml: %v", err)
	}

	// Second call on the same store: no-op.
	res2, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader, FS: fs})
	if err != nil {
		t.Fatalf("BaselineTrust (second): %v", err)
	}
	if res2.Status != "already" || res2.Granted != 0 {
		t.Errorf("second run = {%s, granted:%d}, want {already, 0}", res2.Status, res2.Granted)
	}
	after2, _ := afero.ReadFile(fs, paths.DefaultTrustPath())
	if string(after1) != string(after2) {
		t.Errorf("trust.yaml changed on idempotent re-run:\n--- after1 ---\n%s\n--- after2 ---\n%s", after1, after2)
	}

	// A fresh store reloaded from the persisted file is already baselined, so a
	// new process would also no-op.
	store2, err := trust.New("", trust.WithFS(fs))
	if err != nil {
		t.Fatalf("trust.New (reload): %v", err)
	}
	if !store2.IsBaselined() {
		t.Fatal("reloaded store is not marked baselined")
	}
	res3, err := BaselineTrust(nil, BaselineTrustRequest{Store: store2, Loader: loader, FS: fs})
	if err != nil {
		t.Fatalf("BaselineTrust (reload): %v", err)
	}
	if res3.Status != "already" {
		t.Errorf("reload run status = %q, want already", res3.Status)
	}
}

// TestBaselineTrust_ItemAddedAfterNotGranted proves content pulled after the
// snapshot is absent from the baseline (so it gates once enforcement lands).
func TestBaselineTrust_ItemAddedAfterNotGranted(t *testing.T) {
	store := newTrustStore(t)
	if _, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: baselineSeed()}); err != nil {
		t.Fatalf("BaselineTrust: %v", err)
	}

	// A NEW bundle/item appears after the baseline. Re-running the snapshot must
	// not grant it (the marker makes the second run a no-op).
	extra := map[string]*bundles.Bundle{
		acmeBundle + "extra": {Fragments: map[string]bundles.BundleFragment{"new": {Content: "new body"}}},
	}
	for k, b := range extra {
		b.Name = k
	}
	loader2 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Fragments: map[string]bundles.BundleFragment{"solid": {Content: "solid body", Distilled: ""}}, Name: acmeBundle + "tooling"},
		acmeBundle + "extra":   extra[acmeBundle+"extra"],
	}))

	res, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader2})
	if err != nil {
		t.Fatalf("BaselineTrust (re-run): %v", err)
	}
	if res.Status != "already" {
		t.Errorf("re-run status = %q, want already (no-op)", res.Status)
	}
	if _, ok := store.GrantMatch(trustRepo, "extra#fragments/new", fragHash("new body")); ok {
		t.Error("post-baseline item was granted; it must gate")
	}
	// The original item is still trusted.
	if _, ok := store.GrantMatch(trustRepo, "tooling#fragments/solid", fragHash("solid body")); !ok {
		t.Error("original baselined item lost its grant")
	}
}

// TestBaselineTrust_ContentSwapGates proves that swapping a baselined ref's
// content yields a new effective hash absent from the baseline — so it gates —
// while the originally-snapshotted hash stays trusted (versions coexist).
func TestBaselineTrust_ContentSwapGates(t *testing.T) {
	store := newTrustStore(t)
	loaderV1 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling", Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v1 body"}}},
	}))
	if _, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loaderV1}); err != nil {
		t.Fatalf("BaselineTrust: %v", err)
	}

	// The snapshotted (v1) hash is trusted.
	if _, ok := store.GrantMatch(trustRepo, "tooling#fragments/solid", fragHash("v1 body")); !ok {
		t.Fatal("v1 content not granted by the baseline")
	}
	// The swapped (v2) hash was never snapshotted → no grant → would gate.
	if _, ok := store.GrantMatch(trustRepo, "tooling#fragments/solid", fragHash("v2 body")); ok {
		t.Error("post-swap content hash is granted; a content swap must gate")
	}

	// Re-running the baseline against the swapped content does NOT bless v2
	// (snapshot already taken).
	loaderV2 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling", Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v2 body"}}},
	}))
	if _, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loaderV2}); err != nil {
		t.Fatalf("BaselineTrust (re-run): %v", err)
	}
	if _, ok := store.GrantMatch(trustRepo, "tooling#fragments/solid", fragHash("v2 body")); ok {
		t.Error("re-running the baseline blessed swapped content; the snapshot must run once")
	}
}

// TestBaselineTrust_ItemHashFailureSkipped proves a single item that fails to
// resolve/hash is skipped (counted, warned) without aborting the snapshot — the
// rest of the items are still granted.
func TestBaselineTrust_ItemHashFailureSkipped(t *testing.T) {
	orig := hashBaselineItem
	hashBaselineItem = func(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string) (string, bundles.ContentForm, error) {
		if tRef.Name == "boom" {
			return "", "", fmt.Errorf("simulated hash failure")
		}
		return orig(cfg, loader, tRef, loadRef)
	}
	defer func() { hashBaselineItem = orig }()

	loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{
		acmeBundle + "tooling": {
			Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{
				"solid": {Content: "solid body"},
				"boom":  {Content: "boom body"},
			},
		},
	}))
	store := newTrustStore(t)

	res, err := BaselineTrust(nil, BaselineTrustRequest{Store: store, Loader: loader})
	if err != nil {
		t.Fatalf("BaselineTrust must not abort on a per-item failure: %v", err)
	}
	if res.Status != "baselined" || res.Granted != 1 || res.Failed != 1 {
		t.Errorf("tally = {%s granted:%d failed:%d}, want {baselined 1 1}", res.Status, res.Granted, res.Failed)
	}
	if _, ok := store.GrantMatch(trustRepo, "tooling#fragments/solid", fragHash("solid body")); !ok {
		t.Error("the healthy sibling item was not granted")
	}
}
