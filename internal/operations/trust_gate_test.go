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
			Commands: map[string]bundles.BundleCommand{
				"review":     {Content: "review body"},
				"evilprompt": {Content: "evil prompt body"},
			},
		},
	}
	return seed
}

// gatedAcmeLoader builds a loader over acmeToolingSeed wired to a contentGate
// resolving against records for an UNSIGNED remote bundle, so only review
// states (approved/rejected) decide exposure — everything else is pending
// (withheld).
func gatedAcmeLoader(t *testing.T, records ReviewRecords) (*bundles.Loader, *config.Config) {
	t.Helper()
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	gate := (&contentGate{cfg: cfg, records: records}).allow
	l := bundles.NewLoader(nil, bundles.WithSeededBundles(acmeToolingSeed()), bundles.WithTrustGate(gate))
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
	fx := newTrustFixture(t)
	fx.approveFragment("tooling", "solid", "solid body")
	fx.rejectFragment("tooling", "evil", "evil body")
	loader, cfg := gatedAcmeLoader(t, fx.records())

	res, err := AssembleContext(context.Background(), cfg, AssembleContextRequest{
		Fragments: []string{solidRef, evilRef, swapRef},
		Loader:    loader,
	})
	require.NoError(t, err)

	assert.Contains(t, res.Context, "solid body", "accepted sibling must be present")
	assert.NotContains(t, res.Context, "evil body", "rejected fragment must be withheld")
	assert.NotContains(t, res.Context, "swapped body", "pending (unreviewed) fragment must be withheld")
	// The always-on builtin isolation fragment (resources/builtin_bundles/
	// isolation.yaml) injects unconditionally alongside the loader-resolved
	// set, through this same gate — it is exempt from review (builtin), not
	// exempt from appearing.
	assert.ElementsMatch(t, []string{solidRef, builtinIsolationFragmentRef}, res.FragmentsLoaded)

	withheld := loader.Withheld()
	assert.ElementsMatch(t, []string{evilRef, swapRef}, withheld,
		"both denied refs surfaced (content-free) via Withheld")
}

// TestExposureGate_Resource_GetFragmentWithheld proves the ctxloom://fragments/
// and ctxloom://prompts/ resource path (operations.GetFragment/GetCommand) omits a
// denied item — surfacing the distinct withheld sentinel — while an accepted one
// still resolves.
func TestExposureGate_Resource_GetFragmentWithheld(t *testing.T) {
	fx := newTrustFixture(t)
	fx.approveFragment("tooling", "solid", "solid body")
	fx.rejectFragment("tooling", "evil", "evil body")
	fx.approvePrompt("tooling", "review", "review body")
	fx.rejectPrompt("tooling", "evilprompt", "evil prompt body")
	loader, cfg := gatedAcmeLoader(t, fx.records())

	// Fragment resource.
	_, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: evilRef, Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "denied fragment resource err = %v", err)
	okFrag, err := GetFragment(context.Background(), cfg, GetFragmentRequest{Name: solidRef, Loader: loader})
	require.NoError(t, err)
	assert.Equal(t, "solid body", okFrag.Content)

	// Prompt resource.
	_, err = GetCommand(context.Background(), cfg, GetCommandRequest{Name: acmeBundle + "tooling#commands/evilprompt", Loader: loader})
	assert.True(t, errors.Is(err, errs.ErrCommandWithheld), "denied prompt resource err = %v", err)
	okPrompt, err := GetCommand(context.Background(), cfg, GetCommandRequest{Name: acmeBundle + "tooling#commands/review", Loader: loader})
	require.NoError(t, err)
	assert.Contains(t, okPrompt.Content, "review body")
}

// TestExposureGate_UpdateRegatesExactly proves the review invariant end to end
// at an exposure surface: an accepted version stays exposed; an upstream
// content swap (new hash, no acceptance, untrusted source) returns the item to
// pending and is withheld; accepting the new content re-exposes it.
func TestExposureGate_UpdateRegatesExactly(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})

	v1 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v1 body"}}},
	}
	fx := newTrustFixture(t)
	// A human approved the v1 content (countersigned its bytes).
	fx.approveFragment("tooling", "solid", "v1 body")
	gate := (&contentGate{cfg: cfg, records: fx.records()}).allow

	// v1 stays exposed (accepted at this exact hash).
	l1 := bundles.NewLoader(nil, bundles.WithSeededBundles(v1), bundles.WithTrustGate(gate))
	got, err := l1.GetFragment(acmeBundle + "tooling#fragments/solid", true)
	require.NoError(t, err)
	assert.Equal(t, "v1 body", got.Content)

	// An upstream content swap → new hash, the acceptance no longer matches →
	// pending → withheld.
	v2 := map[string]*bundles.Bundle{
		acmeBundle + "tooling": {Name: acmeBundle + "tooling",
			Fragments: map[string]bundles.BundleFragment{"solid": {Content: "v2 body"}}},
	}
	l2 := bundles.NewLoader(nil, bundles.WithSeededBundles(v2), bundles.WithTrustGate(gate))
	_, err = l2.GetFragment(acmeBundle + "tooling#fragments/solid", true)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "post-swap content must gate, got %v", err)

	// An explicit `ctxloom trust` of the new version re-exposes it.
	fx.approveFragment("tooling", "solid", "v2 body")
	got2, err := l2.GetFragment(acmeBundle + "tooling#fragments/solid", true)
	require.NoError(t, err)
	assert.Equal(t, "v2 body", got2.Content)
}

// TestExposureGate_FailClosed proves the gate withholds on any failure to
// positively justify exposure: an unparseable ref, and a fresh/empty
// review-records store (nothing ever approved), both withhold, never
// default-allow. Unlike the deleted hash-pair trust.yaml, there is no
// "corrupt store" failure mode any more — the countersignature stores degrade
// to "no candidates found", which the decision function already denies by
// construction (step 6, the terminal pending default).
func TestExposureGate_FailClosed(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)

	// Unparseable ref → withhold (resolve error is fail-closed).
	g := &contentGate{cfg: cfg, records: fx.records()}
	assert.False(t, g.allow("garbage-without-selector", pbytes("abc"), "raw", ""),
		"a ref the gate cannot address must be withheld")

	// A fresh, empty records store → every gated item withheld through the
	// loader (nothing has ever been approved).
	empty := &contentGate{records: newTrustFixture(t).records()}
	l := bundles.NewLoader(nil,
		bundles.WithSeededBundles(acmeToolingSeed()),
		bundles.WithTrustGate(empty.allow))
	_, err := l.GetFragment(solidRef, true)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld), "an empty records store must withhold even a would-be-trusted item, got %v", err)
}

// TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds drives the REAL
// exposure path (no injected loader): AssembleContext → exposureLoader →
// buildContentGate. It proves (a) project-local content is exposed via the
// first-party exemption with NO prior trust state (a fresh store — no baseline
// exists in the three-state model), and (b) a rejected item is withheld while
// its sibling is not — all on a virtualized fs (no OS pollution), proving
// cfg.FS() threading.
func TestExposureGate_FullPath_LocalAllowsAndRejectionWithholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// No ssh-agent in this test — SetBlacklist below must degrade to the
	// UNSIGNED path (spec §9.5), never fail the mutation.
	t.Setenv("SSH_AUTH_SOCK", "")
	fs := afero.NewMemMapFs()
	appDir := "/proj/.ctxloom"
	bundlesDir := paths.LocalBundlesPath(appDir)
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
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	gate := &contentGate{cfg: cfg, records: newTrustFixture(t).records()}

	// "https://github.com/acme/repo" is missing "@bundles/<name>" — it fails
	// remote.ParseReference (parseHTTPSReference: "URL reference missing item
	// path"), but it is unmistakably an ATTEMPTED canonical ref, not a bare
	// local bundle name.
	const unrecognizedSourceRef = "https://github.com/acme/repo"
	ref := unrecognizedSourceRef + "#fragments/evil"

	allowed := gate.allow(ref, []byte("evil body"), rawForm, "")
	assert.False(t, allowed,
		"content seeded under an unrecognized source ref must be withheld, never silently treated as local")
}

// TestExposureGate_SessionStartRegen_Withholds proves the SessionStart regen
// entrypoint (ApplyHooks RegenerateContext → regenerateContext → exposureLoader)
// withholds a rejected fragment from the injected context file while keeping
// its sibling.
func TestExposureGate_SessionStartRegen_Withholds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("SSH_AUTH_SOCK", "")
	tmpDir := t.TempDir()
	appDir := filepath.Join(tmpDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
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

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	if _, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: "dev#fragments/blocked"}); err != nil {
		t.Fatalf("SetBlacklist: %v", err)
	}

	mockConfigLoader := func() (*config.Config, error) {
		return config.NewFixture(config.Fixture{
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
		}), nil
	}

	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
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

// TestContentGate_RecordsEveryDenyWithAReason pins the refutation of a
// proposal that read contentGate as two types — a trust gate and a
// withheld-item ledger — and proposed splitting them. The tally is not a
// second concern with a life of its own: it is the gate's record of ITS OWN
// decisions, written on every deny by the same call that made them, and
// read only through the gate value its builder returned. What makes it
// load-bearing is the mandate that a
// withhold is never silent or reasonless (docs/trust-model.md): the executable
// surfaces reach the gate directly with no loader to tally them, so if the gate
// did not record, nothing else could.
//
// So the invariant to hold is that EVERY deny arm records, including the two
// that never reach the decision function at all — a ref the gate cannot
// address, and an evaluation that fails — since those are exactly the arms a
// split would have to remember to keep wired.
func TestContentGate_RecordsEveryDenyWithAReason(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	g := &contentGate{cfg: cfg, records: fx.records()}

	const unaddressable = "garbage-without-selector"
	require.False(t, g.allow(unaddressable, pbytes("abc"), rawForm, ""))
	require.False(t, g.allow(solidRef, pbytes("never-approved"), rawForm, ""))

	byRef := map[string]EffectiveTrustResult{}
	for _, it := range g.withheldItems() {
		byRef[it.Ref] = it.Result
	}
	require.Contains(t, byRef, unaddressable,
		"a ref the gate could not even parse must still be tallied — nothing downstream can report a withhold the gate did not record")
	require.Contains(t, byRef, solidRef)

	for ref, res := range byRef {
		assert.False(t, res.Trusted(), "only denials are recorded (%s)", ref)
		assert.NotEmpty(t, res.Reason(), "every withheld item must carry a reason a user can act on (%s)", ref)
	}
	assert.ElementsMatch(t, []string{unaddressable, solidRef}, g.withheldRefs(),
		"withheldRefs and withheldItems must describe the same set")
}

// TestParseTrustItemRef_AttemptedSourceRefsFailClosed pins the half of
// parseTrustItemRef's fail-open boundary that is CORRECT, so it cannot regress
// while the remaining gap is adjudicated separately. A string carrying a
// scheme marker that nonetheless fails remote.ParseReference must ERROR rather
// than be downgraded to a first-party local bundle name: every caller treats
// the error as fail-closed, so erroring withholds, while the downgrade would
// hand the item the step-3 local exemption and skip review entirely.
//
// The counterpart — a bare token with no scheme marker at all IS a local bundle
// name — is asserted alongside, because a boundary that only ever fails closed
// would break every project whose bundles are its own.
func TestParseTrustItemRef_AttemptedSourceRefsFailClosed(t *testing.T) {
	for _, base := range []string{
		"https://github.com/acme/repo", // canonical URL missing @bundles/<name>
		"git@github.com:acme/repo",     // ssh ref missing the item path
		"ctxloom:local@",               // local ref missing its item path
	} {
		_, _, _, err := parseTrustItemRef(base + "#fragments/x")
		assert.Error(t, err, "%q looks like an attempted source ref and must fail closed, never resolve as a local bundle name", base)
	}

	tRef, _, _, err := parseTrustItemRef("my-tools#fragments/x")
	require.NoError(t, err, "a bare bundle name carries no scheme marker and is genuinely local")
	assert.True(t, tRef.IsLocal)
	assert.Equal(t, "my-tools", tRef.Bundle)
}
