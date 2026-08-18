package operations

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestBuiltinFragment_RejectViaLoaderRoute_WithholdsInjectionRoute is the
// load-bearing test crispy-scoop asks for: reject a builtin item via the
// ROUTE A identity (resolved through the loader by ref — what a profile
// selecting "isolation#fragments/isolation-axes" produces, and what
// `run -r cleanup` needs) and prove it is withheld via the INDEPENDENT ROUTE
// B production entry point (config.ResolveBuiltinBundleFragments's
// unconditional injection) — not by comparing ref strings, but by calling
// both real delivery chokes and reading what the gate actually decided.
//
// Before bundles.localFSReader.readBundle stamped bundle.sourceRef for a
// ProvenanceBuiltin read, route A's TrustRef fell back to the bare bundle
// name ("isolation#fragments/isolation-axes"), which trust.ParseItemRef's
// bare-token fallback resolves to IsLocal — a DIFFERENT trust.Ref than route
// B's "builtin:isolation#fragments/isolation-axes" (IsBuiltin). Rejecting via
// one left the other deliverable: exactly the gate bypass this fixes.
func TestBuiltinFragment_RejectViaLoaderRoute_WithholdsInjectionRoute(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	gate := &contentGate{cfg: cfg, records: fx.records()}

	// Route A's loader: NewBuiltinReader composed directly into a Loader, the
	// same reader/loader machinery a profile's ref-selection resolves a
	// builtin bundle through (Config.BundleLoader does not wire this reader
	// in today — a separate, not-yet-made change — but the reader and its
	// TrustRef derivation are real production code, exercised here exactly as
	// they run).
	loader := bundles.NewLoader(bundles.NewBuiltinReader())
	pipe := bundles.NewPipeline(loader, gate, true)

	// Sanity: BOTH routes deliver before any rejection — a builtin is exempt
	// from review by default, so an empty records store must not withhold it.
	// This rules out "the assertion below passes because the item was
	// unreachable anyway."
	before, err := pipe.GetFragment("isolation#fragments/isolation-axes")
	require.NoError(t, err, "route A must deliver the builtin fragment before any rejection")
	require.NotEmpty(t, before.Content)

	beforeFrags := cfg.ResolveBuiltinBundleFragments(gate)
	require.True(t, containsFragmentRef(beforeFrags, builtinIsolationFragmentRef),
		"route B must deliver the builtin fragment before any rejection")

	// Reject via ROUTE A's own identity: read the item the way route A does,
	// and reject the exact trust.Ref that read carries — the ref a human
	// reviewing content resolved BY REF through the loader would be shown and
	// would reject.
	//
	// REF-SCOPED ONLY (fx.rejectRef, not fx.rejectItem/rejectContent): a
	// content-scoped rejection is deliberately REF-AGNOSTIC (spec §5.3 — it
	// follows identical bytes wherever they appear, by design, so a
	// renamed/moved copy stays rejected) and would withhold route B by
	// matching payload bytes even WITHOUT the identity fix, masking exactly
	// the bug this test exists to catch. The ref-level block is the one whose
	// reach depends on the two routes keying the SAME trust.Ref.
	items, err := loader.ReadFragment("isolation#fragments/isolation-axes")
	require.NoError(t, err)
	require.Len(t, items, 1)
	tRefA := mustParseProducerRef(t, items[0].TrustRef)
	fx.rejectRef(tRefA)

	// ROUTE A now withholds (expected — same ref rejected).
	_, err = pipe.GetFragment("isolation#fragments/isolation-axes")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"route A must withhold the item it was rejected through, got %v", err)

	// THE LOAD-BEARING ASSERTION: ROUTE B — an entirely independent call,
	// through config.ResolveBuiltinBundleFragments's injection path, which
	// never touched loader or items above — must ALSO withhold it now.
	afterFrags := cfg.ResolveBuiltinBundleFragments(gate)
	assert.False(t, containsFragmentRef(afterFrags, builtinIsolationFragmentRef),
		"a builtin rejected via the loader-resolved route must be withheld via the injection route too — "+
			"two trust identities for one item is the gate bypass crispy-scoop describes")
}

func containsFragmentRef(frags []config.BuiltinFragment, ref string) bool {
	for _, f := range frags {
		if f.Name == ref {
			return true
		}
	}
	return false
}

// TestBuiltinFragment_Rejection_PersistsAcrossReload proves a builtin
// rejection is not an artifact of one in-process gate: re-opening the
// countersignature store (fresh countersignRecords, fresh contentGate, fresh
// Loader/Pipeline — nothing shared with the objects that recorded the
// rejection) still withholds the item. This is what distinguishes "rejected"
// from "happened to be denied by an object still holding it in memory."
func TestBuiltinFragment_Rejection_PersistsAcrossReload(t *testing.T) {
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)

	loader := bundles.NewLoader(bundles.NewBuiltinReader())
	items, err := loader.ReadFragment("isolation#fragments/isolation-axes")
	require.NoError(t, err)
	tRef := mustParseProducerRef(t, items[0].TrustRef)

	// Reject once, through a gate built over the fixture's stores. Ref-scoped
	// only — see the sibling test for why a content-scoped (ref-agnostic)
	// reject would pass regardless of the identity fix and prove nothing.
	firstGate := &contentGate{cfg: cfg, records: fx.records()}
	fx.rejectRef(tRef)
	_, err = bundles.NewPipeline(loader, firstGate, true).GetFragment("isolation#fragments/isolation-axes")
	require.True(t, errors.Is(err, errs.ErrFragmentWithheld), "sanity: rejection must take effect immediately")

	// "Reload": build a BRAND NEW countersignRecords over the SAME backing
	// stores (re-reads the fixture's memFs from scratch, like a fresh process
	// re-opening ~/.ctxloom/approvals would), a brand new gate, and a brand
	// new loader — none of it the object identity that recorded the reject.
	reloadedRecords := fx.records()
	reloadedGate := &contentGate{cfg: cfg, records: reloadedRecords}
	reloadedLoader := bundles.NewLoader(bundles.NewBuiltinReader())

	_, err = bundles.NewPipeline(reloadedLoader, reloadedGate, true).GetFragment("isolation#fragments/isolation-axes")
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a builtin rejection must survive a reload of the gate/loader, got %v", err)

	afterReloadFrags := cfg.ResolveBuiltinBundleFragments(reloadedGate)
	assert.False(t, containsFragmentRef(afterReloadFrags, builtinIsolationFragmentRef),
		"the injection route must also still withhold it after reload")
}

// TestClassify_BuiltinItem_KeysTrustDecisionOnSourceRefNotResolutionRef pins
// review.go's own fix to the SAME identity bug crispy-scoop closed elsewhere:
// classify used to build its trust decision from bundleRef — read.Ref(), the
// bundle's BARE resolution name ("isolation") — reparsed through
// trust.ParseItemRef's bare-token fallback, landing on Ref{IsLocal: true}.
// The honest identity is read.SourceRef() ("ctxloom+builtin:isolation"),
// Ref{IsBuiltin: true}.
//
// There is currently NO observable difference in review's own output from
// this fix (a builtin auto-allows under EITHER identity, so it never reaches
// NeedsReview() either way — see the doc on classify's tRef construction), so
// this test pins the fix at its own point of divergence: the trust.Ref
// classify's authorizer.Admit call is actually handed, captured directly
// rather than inferred from a downstream verdict.
func TestClassify_BuiltinItem_KeysTrustDecisionOnSourceRefNotResolutionRef(t *testing.T) {
	loader := bundles.NewLoader(bundles.NewBuiltinReader())
	read, ok := loader.Read("isolation")
	require.True(t, ok, "the embedded builtin must resolve by its bare name")

	var seenRef trust.Ref
	captured := bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		seenRef = e.Ref
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonBuiltin}
	})
	e := &reviewEnumerator{authorizer: captured}

	frag, ok := read.Bundle.Fragments["isolation-axes"]
	require.True(t, ok, "the embedded isolation bundle must ship isolation-axes")
	payload, form := frag.ContentPayload(false)

	// read.Ref() ("isolation") is passed as bundleRef exactly as pendingItems
	// passes it — classify must NOT use it to build the trust decision.
	_, _ = e.classify(read.Ref(), "fragments", "isolation-axes", read, payload, string(form), false)

	assert.True(t, seenRef.IsBuiltin,
		"classify must key the trust decision on the bundle's typed SourceRef (IsBuiltin), not its bare resolution ref (which trust.ParseItemRef's bare-token fallback reads as IsLocal)")
	assert.False(t, seenRef.IsLocal)
	assert.Equal(t, "isolation", seenRef.Bundle)
}

// TestNonBuiltinLocalBundle_TrustRefUnchanged is the regression guard: a
// project-authored (NewProjectReader) bundle's content trust ref must be
// completely unaffected by the builtin-only stamp in
// localFSReader.readBundle. If it were affected, that would be the widening
// crispy-scoop's guard-rails forbid — a local item picking up a builtin-like
// (or any different) identity. It must still resolve bare, to IsLocal, exactly
// as before this change.
func TestNonBuiltinLocalBundle_TrustRefUnchanged(t *testing.T) {
	seed := map[string]*bundles.Bundle{
		"dev": {
			Name: "dev",
			Fragments: map[string]bundles.BundleFragment{
				"keep": {Content: "KEEP-MARKER"},
			},
		},
	}
	loader := seedLoader(t, seed)
	items, err := loader.ReadFragment("dev#fragments/keep")
	require.NoError(t, err)
	require.Len(t, items, 1)

	const wantRef = "ctxloom+local:dev#fragments/keep"
	assert.Equal(t, wantRef, items[0].TrustRef,
		"a project-local bundle's TrustRef must stay keyed on the bare, unprefixed name")

	tRef := mustParseProducerRef(t, items[0].TrustRef)
	assert.True(t, tRef.IsLocal, "a project-local item must still resolve to IsLocal")
	assert.False(t, tRef.IsBuiltin, "a project-local item must never resolve to IsBuiltin")
	assert.Equal(t, "ctxloom:local", tRef.CanonicalURL())
}
