package operations

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/shared/agent"
)

// acmeToolingSeed seeds one REMOTE bundle (acme tooling) with fragments and
// prompts, keyed by its canonical ref so AssembleContext's ResolveFragmentAsk
// resolves to it and the gate ref matches the baseline/grant key shape.
func acmeToolingSeed() map[string]*bundles.Bundle {
	seed := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {
			Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{
				"solid":   {Content: "solid body"},
				"evil":    {Content: "evil body"},
				"swapped": {Content: "swapped body"},
			},
			Prompts: map[string]bundles.BundlePrompt{
				"review":     {Content: "review body"},
				"evilprompt": {Content: "evil prompt body"},
			},
		},
	}
	return seed
}

// gatedAcmeLoader builds a loader over acmeToolingSeed wired to a contentGate
// resolving against store + an untrusted acme remote, so only grants/blacklist/
// denylist decide exposure (the remote tier denies by default).
func gatedAcmeLoader(t *testing.T, store *trust.Store) (*bundles.Loader, *config.Config) {
	t.Helper()
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow
	l := bundles.NewLoader(nil, true, bundles.WithSeededBundles(acmeToolingSeed()), bundles.WithTrustGate(gate))
	return l, cfg
}

const (
	solidRef = acmeBundle + "tooling#fragments/solid"
	evilRef  = acmeBundle + "tooling#fragments/evil"
	swapRef  = acmeBundle + "tooling#fragments/swapped"
)

// TestExposureGate_AssembleContext_WithholdsDenied proves the assembly surface
// omits a blacklisted fragment AND an untrusted (grant-less) one, keeps a granted
// sibling, and surfaces the withheld refs content-free via loader.Withheld().
func TestExposureGate_AssembleContext_WithholdsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTrustStore(t)
	if err := store.AddGrant(trustRepo, "tooling#fragments/solid", fragHash("solid body"), "raw", ""); err != nil {
		t.Fatalf("AddGrant: %v", err)
	}
	if err := store.Blacklist(trustRepo, "tooling#fragments/evil", fragHash("evil body")); err != nil {
		t.Fatalf("Blacklist: %v", err)
	}
	loader, cfg := gatedAcmeLoader(t, store)

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{solidRef, evilRef, swapRef},
		Loader:    loader,
	})
	require.NoError(t, err)

	assert.Contains(t, res.Context, "solid body", "granted sibling must be present")
	assert.NotContains(t, res.Context, "evil body", "blacklisted fragment must be withheld")
	assert.NotContains(t, res.Context, "swapped body", "untrusted grant-less fragment must be withheld")
	assert.Len(t, res.FragmentsLoaded, 1)

	withheld := loader.Withheld()
	assert.ElementsMatch(t, []string{evilRef, swapRef}, withheld,
		"both denied refs surfaced (content-free) via Withheld")
}

// TestExposureGate_Resource_GetFragmentWithheld proves the ctxloom://fragments/
// and ctxloom://prompts/ resource path (operations.GetFragment/GetPrompt) omits a
// denied item — surfacing the distinct withheld sentinel — while a trusted one
// still resolves.
func TestExposureGate_Resource_GetFragmentWithheld(t *testing.T) {
	store := newTrustStore(t)
	require.NoError(t, store.AddGrant(trustRepo, "tooling#fragments/solid", fragHash("solid body"), "raw", ""))
	require.NoError(t, store.Blacklist(trustRepo, "tooling#fragments/evil", fragHash("evil body")))
	require.NoError(t, store.AddGrant(trustRepo, "tooling#prompts/review", promptHash("review body"), "raw", ""))
	require.NoError(t, store.Blacklist(trustRepo, "tooling#prompts/evilprompt", promptHash("evil prompt body")))
	loader, cfg := gatedAcmeLoader(t, store)

	// Fragment resource.
	_, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: evilRef, Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "denied fragment resource err = %v", err)
	okFrag, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: solidRef, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "solid body", okFrag.Content)

	// Prompt resource.
	_, err = GetPrompt(context.Background(), cfg, GetPromptRequest{Name: acmeBundle + "tooling#prompts/evilprompt", Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrPromptWithheld), "denied prompt resource err = %v", err)
	okPrompt, err := GetPrompt(context.Background(), cfg, GetPromptRequest{Name: acmeBundle + "tooling#prompts/review", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, okPrompt.Content, "review body")
}

// TestExposureGate_BaselineInterplay proves the migration contract end to end at
// an exposure surface: a baselined (pre-existing) version stays exposed; a
// post-baseline content swap (new hash ∉ baseline, no grant, untrusted remote) is
// withheld; an explicit grant for the new hash then re-exposes it.
func TestExposureGate_BaselineInterplay(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})

	v1 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v1 body"}}},
	}
	store := newTrustStore(t)
	// Baseline the v1 content (mints a grant for its effective hash).
	if _, err := BaselineTrust(cfg, BaselineTrustRequest{Store: store, Loader: bundles.NewLoader(nil, true, bundles.WithSeededBundles(v1))}); err != nil {
		t.Fatalf("BaselineTrust: %v", err)
	}
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow

	// v1 stays exposed (baselined).
	l1 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(v1), bundles.WithTrustGate(gate))
	got, err := l1.GetFragment(acmeBundle + "tooling#fragments/solid")
	require.NoError(t, err)
	assert.Equal(t, "v1 body", got.Content)

	// A post-baseline content swap → new hash ∉ baseline, no grant → withheld.
	v2 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v2 body"}}},
	}
	l2 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(v2), bundles.WithTrustGate(gate))
	_, err = l2.GetFragment(acmeBundle + "tooling#fragments/solid")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "post-swap content must gate, got %v", err)

	// An explicit `ctxloom trust` of the new version re-exposes it.
	require.NoError(t, store.AddGrant(trustRepo, "tooling#fragments/solid", fragHash("v2 body"), "raw", ""))
	got2, err := l2.GetFragment(acmeBundle + "tooling#fragments/solid")
	require.NoError(t, err)
	assert.Equal(t, "v2 body", got2.Content)
}

// TestExposureGate_FailClosed proves the gate withholds on any failure to
// positively justify exposure: an unparseable ref and a denyAll (unreadable
// store) both withhold, never default-allow.
func TestExposureGate_FailClosed(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}

	// Unparseable ref → withhold (resolve error is fail-closed).
	g := &contentGate{cfg: cfg, store: newTrustStore(t)}
	assert.False(t, g.allow("garbage-without-selector", "sha256:abc", "raw"),
		"a ref the gate cannot address must be withheld")

	// denyAll (store unreadable) → every gated item withheld through the loader.
	deny := &contentGate{denyAll: true}
	l := bundles.NewLoader(nil, true,
		bundles.WithSeededBundles(acmeToolingSeed()),
		bundles.WithTrustGate(deny.allow))
	_, err := l.GetFragment(solidRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "denyAll must withhold even a would-be-trusted item, got %v", err)
}

// TestExposureGate_FullPath_BaselineRunsAndBlacklistWithholds drives the REAL
// exposure path (no injected loader): AssembleContext → exposureLoader →
// newContentGate. It proves (a) the baseline auto-runs on this entrypoint so
// pre-existing local content stays exposed (follow-up #4), and (b) a blacklisted
// item is withheld while its sibling is not — all on a virtualized fs (no OS
// pollution), proving cfg.FS() threading.
func TestExposureGate_FullPath_BaselineRunsAndBlacklistWithholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	bundlesDir := paths.BundlesPath(appDir)
	require.NoError(t, fs.MkdirAll(bundlesDir, 0o755))
	bundleYAML := `version: "1.0"
description: local dev
fragments:
  keep:
    content: |
      KEEP-MARKER
  blocked:
    content: |
      BLOCKED-MARKER
`
	require.NoError(t, afero.WriteFile(fs, bundlesDir+"/dev.yaml", []byte(bundleYAML), 0o644))

	cfg, err := config.Load(config.WithFS(fs), config.WithAppDir(appDir))
	require.NoError(t, err)

	// Blacklist one local fragment (beats the local auto-allow).
	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked", FS: fs}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/keep", "dev#fragments/blocked"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "KEEP-MARKER", "trusted local sibling must be present")
	assert.NotContains(t, res.Context, "BLOCKED-MARKER", "blacklisted local fragment must be withheld")

	// The gate ran the baseline on this entrypoint (marker persisted to memfs).
	reread, err := trust.New(paths.TrustPath(appDir), trust.WithFS(fs))
	require.NoError(t, err)
	assert.True(t, reread.IsBaselined(), "baseline must have run via the exposure gate")
}

// TestExposureGate_SessionStartRegen_Withholds proves the SessionStart regen
// entrypoint (ApplyHooks RegenerateContext → regenerateContext → exposureLoader)
// withholds a blacklisted fragment from the injected context file while keeping
// its sibling.
func TestExposureGate_SessionStartRegen_Withholds(t *testing.T) {
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "cache", "bundles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))

	bundleContent := `version: "1.0"
description: Test bundle
fragments:
  keep:
    content: |
      KEEP-MARKER
  blocked:
    content: |
      BLOCKED-MARKER
`
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "dev.yaml"), []byte(bundleContent), 0o644))

	cfg := &config.Config{AppPaths: []string{appDir}}
	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked"}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	mockConfigLoader := func() (*config.Config, error) {
		return &config.Config{
			AppPaths: []string{appDir},
			Profiles: config.ProfilesConfig{
				Defaults: []string{"default"},
				Definitions: map[string]config.Profile{
					"default": {Fragments: []config.FragmentRef{
						{Name: "dev#fragments/keep"},
						{Name: "dev#fragments/blocked"},
					}},
				},
			},
		}, nil
	}

	result, err := ApplyHooks(context.Background(), nil, ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ExecPath:          "/usr/bin/ctxloom",
		ConfigLoader:      mockConfigLoader,
		WorkDir:           tmpDir,
	})
	require.NoError(t, err)
	require.NotEmpty(t, result.ContextHash)

	data, err := os.ReadFile(filepath.Join(tmpDir, agent.SCMContextSubdir, result.ContextHash+".md"))
	require.NoError(t, err)
	assert.Contains(t, string(data), "KEEP-MARKER", "trusted sibling must be in the regenerated context")
	assert.NotContains(t, string(data), "BLOCKED-MARKER", "blacklisted fragment must be withheld from SessionStart regen")
}
