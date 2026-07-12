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

	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// acmeToolingSeed seeds one REMOTE bundle (acme tooling) with fragments and
// prompts, keyed by its canonical ref so AssembleContext's ResolveFragmentAsk
// resolves to it and the gate ref matches the review-state key shape.
func acmeToolingSeed() map[string]*bundles.Bundle {
	seed := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {
			Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{
				"solid":   {Content: "solid body"},
				"evil":    {Content: "evil body"},
				"swapped": {Content: "swapped body"},
			},
			Skills: map[string]bundles.BundleSkill{
				"review":     {Content: "review body"},
				"evilprompt": {Content: "evil prompt body"},
			},
		},
	}
	return seed
}

// gatedAcmeLoader builds a loader over acmeToolingSeed wired to a contentGate
// resolving against store + an untrusted acme remote, so only review states
// (accepted/rejected) decide exposure — everything else is pending (withheld).
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
// omits a rejected fragment AND a pending (never-reviewed) one, keeps an
// accepted sibling, and surfaces the withheld refs content-free via
// loader.Withheld().
func TestExposureGate_AssembleContext_WithholdsDenied(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	store := newTrustStore(t)
	if err := store.SetAccepted(trustRepo, "tooling#fragments/solid", fragHash("solid body"), ""); err != nil {
		t.Fatalf("SetAccepted: %v", err)
	}
	if err := store.SetRejected(trustRepo, "tooling#fragments/evil", fragHash("evil body")); err != nil {
		t.Fatalf("SetRejected: %v", err)
	}
	loader, cfg := gatedAcmeLoader(t, store)

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{solidRef, evilRef, swapRef},
		Loader:    loader,
	})
	require.NoError(t, err)

	assert.Contains(t, res.Context, "solid body", "accepted sibling must be present")
	assert.NotContains(t, res.Context, "evil body", "rejected fragment must be withheld")
	assert.NotContains(t, res.Context, "swapped body", "pending (unreviewed) fragment must be withheld")
	assert.Len(t, res.FragmentsLoaded, 1)

	withheld := loader.Withheld()
	assert.ElementsMatch(t, []string{evilRef, swapRef}, withheld,
		"both denied refs surfaced (content-free) via Withheld")
}

// TestExposureGate_Resource_GetFragmentWithheld proves the ctxloom://fragments/
// and ctxloom://prompts/ resource path (operations.GetFragment/GetSkill) omits a
// denied item — surfacing the distinct withheld sentinel — while an accepted one
// still resolves.
func TestExposureGate_Resource_GetFragmentWithheld(t *testing.T) {
	store := newTrustStore(t)
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#fragments/solid", fragHash("solid body"), ""))
	require.NoError(t, store.SetRejected(trustRepo, "tooling#fragments/evil", fragHash("evil body")))
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#prompts/review", promptHash("review body"), ""))
	require.NoError(t, store.SetRejected(trustRepo, "tooling#prompts/evilprompt", promptHash("evil prompt body")))
	loader, cfg := gatedAcmeLoader(t, store)

	// Fragment resource.
	_, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: evilRef, Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "denied fragment resource err = %v", err)
	okFrag, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: solidRef, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "solid body", okFrag.Content)

	// Prompt resource.
	_, err = GetSkill(context.Background(), cfg, GetSkillRequest{Name: acmeBundle + "tooling#skills/evilprompt", Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrSkillWithheld), "denied prompt resource err = %v", err)
	okPrompt, err := GetSkill(context.Background(), cfg, GetSkillRequest{Name: acmeBundle + "tooling#skills/review", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, okPrompt.Content, "review body")
}

// TestExposureGate_UpdateRegatesExactly proves the review invariant end to end
// at an exposure surface: an accepted version stays exposed; an upstream
// content swap (new hash, no acceptance, untrusted source) returns the item to
// pending and is withheld; accepting the new content re-exposes it.
func TestExposureGate_UpdateRegatesExactly(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	reg := newRegistry(t, remoteSpec{name: "acme", url: trustRepo, trust: false})

	v1 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v1 body"}}},
	}
	store := newTrustStore(t)
	// A human accepted the v1 content (records its hash).
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#fragments/solid", fragHash("v1 body"), ""))
	gate := (&contentGate{cfg: cfg, store: store, registry: reg}).allow

	// v1 stays exposed (accepted at this exact hash).
	l1 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(v1), bundles.WithTrustGate(gate))
	got, err := l1.GetFragment(acmeBundle + "tooling#fragments/solid")
	require.NoError(t, err)
	assert.Equal(t, "v1 body", got.Content)

	// An upstream content swap → new hash, the acceptance no longer matches →
	// pending → withheld.
	v2 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v2 body"}}},
	}
	l2 := bundles.NewLoader(nil, true, bundles.WithSeededBundles(v2), bundles.WithTrustGate(gate))
	_, err = l2.GetFragment(acmeBundle + "tooling#fragments/solid")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "post-swap content must gate, got %v", err)

	// An explicit `ctxloom trust` of the new version re-exposes it.
	require.NoError(t, store.SetAccepted(trustRepo, "tooling#fragments/solid", fragHash("v2 body"), ""))
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

// TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds drives the REAL
// exposure path (no injected loader): AssembleContext → exposureLoader →
// newContentGate. It proves (a) project-local content is exposed via the
// first-party exemption with NO prior trust state (a fresh store — no baseline
// exists in the three-state model), and (b) a rejected item is withheld while
// its sibling is not — all on a virtualized fs (no OS pollution), proving
// cfg.FS() threading.
func TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds(t *testing.T) {
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

	// Reject one local fragment (rejection beats the local exemption).
	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked", FS: fs}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{"dev#fragments/keep", "dev#fragments/blocked"},
	})
	require.NoError(t, err)
	assert.Contains(t, res.Context, "KEEP-MARKER", "local sibling must be present (first-party exemption, no review state needed)")
	assert.NotContains(t, res.Context, "BLOCKED-MARKER", "rejected local fragment must be withheld")
}

// TestContentGate_UnrecognizedSourceRef_FailsClosed proves the fail-open bug
// in parseTrustItemRef's fallback: a bundle seeded under a source ref that
// SUPERFICIALLY looks like a canonical URL but fails remote.ParseReference
// (here: missing the required "@<type>/<path>" suffix) must NOT be silently
// downgraded to "local" — that bypasses the trust gate and review entirely.
// A genuinely local bundle (a bare name, no scheme marker at all — see
// TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds and the
// "plain local bundle name" case in TestParseTrustItemRef) must keep working;
// this test is the companion proving the OTHER half: a malformed/unrecognized
// scheme-qualified ref must fail closed instead.
func TestContentGate_UnrecognizedSourceRef_FailsClosed(t *testing.T) {
	cfg := &config.Config{AppPaths: []string{testBaseDir}}
	reg := newRegistry(t) // no remotes registered/trusted
	gate := &contentGate{cfg: cfg, store: newTrustStore(t), registry: reg}

	// "https://github.com/acme/repo" is missing "@bundles/<name>" — it fails
	// remote.ParseReference (parseHTTPSReference: "URL reference missing item
	// path"), but it is unmistakably an ATTEMPTED canonical ref, not a bare
	// local bundle name.
	const unrecognizedSourceRef = "https://github.com/acme/repo"
	ref := unrecognizedSourceRef + "#fragments/evil"

	allowed := gate.allow(ref, fragHash("evil body"), rawForm)
	assert.False(t, allowed,
		"content seeded under an unrecognized source ref must be withheld, never silently treated as local")
}

// TestExposureGate_SessionStartRegen_Withholds proves the SessionStart regen
// entrypoint (ApplyHooks RegenerateContext → regenerateContext → exposureLoader)
// withholds a rejected fragment from the injected context file while keeping
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
			AppPaths:     []string{appDir},
			DefaultAgent: "default",
			Agents:       map[string]agents.Agent{"default": {Profiles: []string{"default"}}},
			Profiles: config.ProfilesConfig{
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
	assert.Contains(t, string(data), "KEEP-MARKER", "the local sibling must be in the regenerated context")
	assert.NotContains(t, string(data), "BLOCKED-MARKER", "the rejected fragment must be withheld from SessionStart regen")
}
