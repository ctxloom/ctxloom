package operations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// mustParseProducerRef parses ref — an item ref a MIGRATED producer emitted
// (ItemRead.TrustRef, LoadedSkill.TrustRef, config's BuiltinFragment.Name, or
// a ref built by hand through trust.BundleRef.WithItem in a test) — into the
// trust.Ref shape a test wants to feed to a fixture's rejectRef/blacklist
// helper or assert IsLocal/IsBuiltin/RepoURL/Key() against.
//
// It is the TEST-SIDE analog of bundles.Decide's own step 4 switch
// (trust.ParseBundleRef + trust.RefFromBundleRef) rather than
// trust.ParseItemRef: every producer this slice migrates now emits the
// canonical "ctxloom+<class>:...#<kind>/<item>" grammar, which
// trust.ParseItemRef does not understand (it stays as the OLD grammar's
// parser throughout this slice — only Decide's own internal call switches).
// A test asserting against a migrated producer's literal output must switch
// with it, or its assertion is pinned to a grammar the producer no longer
// speaks.
func mustParseProducerRef(t *testing.T, ref string) trust.Ref {
	t.Helper()
	br, err := trust.ParseBundleRef(ref)
	require.NoError(t, err, "ref %q must parse as the canonical bundle-reference grammar", ref)
	return trust.RefFromBundleRef(br)
}

// mustLocalItemRef mints a canonical LOCAL-class item ref for a bare bundle
// name — the shape bundles.Decide itself now parses, for a hand-built (not
// producer-derived) fixture ref this package feeds directly to
// Decide/admitFragment/admitExec.
func mustLocalItemRef(bundle string, kind trust.ItemKind, item string) string {
	br, err := trust.LocalRef(bundle)
	if err != nil {
		panic(err)
	}
	full, err := br.WithItem(kind, item)
	if err != nil {
		panic(err)
	}
	return full.String()
}

// canonicalWithheldRef converts an OLD-grammar item ref — the shape a fixture
// constant built by hand as "<source>#<kind>/<name>" for use as an ASK/seed
// string (Catalog.Lookup and seedLoader still speak only that grammar; this
// slice does not touch them) — into the canonical bundle-reference grammar
// string a migrated producer now emits for the IDENTICAL item, so a test
// written before the migration can assert Pipeline.Withheld()'s literal tally
// without hand-deriving the new grammar's host-splitting/escaping rules a
// second, competing way. It reuses trust.ParseItemRef (unaffected by this
// slice — only bundles.Decide's own internal call switches) to decompose the
// old-grammar string, then Ref.AsBundleRef to re-mint it — the exact
// bridge ItemRefFor/fragmentRead use in production.
func canonicalWithheldRef(t *testing.T, oldGrammarRef string) string {
	t.Helper()
	tRef, _, _, err := trust.ParseItemRef(oldGrammarRef)
	require.NoError(t, err, "fixture ref %q must be valid old-grammar for the conversion to mean anything", oldGrammarRef)
	br, err := tRef.AsBundleRef()
	require.NoError(t, err)
	return br.String()
}

// testAuthorizer is the two-valued Authorizer a test reaches for when it is exercising
// what happens AROUND a decision rather than the decision itself: admit
// everything, or withhold everything. The Reasons are the plainest true ones —
// a blanket admit is not claiming a provenance it checked, and a blanket
// withhold is the ordinary pending state.
func testAuthorizer(admit bool) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}

// The Authorizer takes an Exposure carrying the READ, not a signer string,
// because a signer string cannot say whether a signature covered its bytes.
// These two helpers build that read for the tests below, which only have a
// principal to pass.

// execRead is the read of a pinned REMOTE bundle at acmeBundle+"tooling": one
// nothing has signed when principal is empty, one signed by a key this machine
// trusts to publish as principal otherwise.
//
// Signed FOR REAL, not stamped: "a trusted publisher signed these bytes" is the
// fact these tests turn on, and a stamped string is not that fact.
func execRead(t *testing.T, principal string) bundles.BundleRead {
	t.Helper()
	const ref = acmeBundle + "tooling"
	b := &bundles.Bundle{Version: "1.0", Fragments: map[string]bundles.BundleFragment{"f": {Content: "x"}}}
	if principal == "" {
		return readOf(t, seedLoader(t, map[string]*bundles.Bundle{ref: b}), ref)
	}
	return readOf(t, seedTrustedSigned(t, ref, principal, b), ref)
}

// admitExec runs one executable item through the authorizer with that read, and
// reports whether it may be delivered.
func admitExec(t *testing.T, g *contentGate, read bundles.BundleRead, ref string, payload []byte, form string) bool {
	t.Helper()
	return bundles.Decide(g, read, ref, payload, bundles.ContentForm(form)).Allow
}

// postureCtxOf spells the READ POSTURE a decision-table row's ref implies, so a
// table that is about the CASCADE does not have to restate it per row.
//
// It is a test harness and nothing else. Production never derives posture from
// a ref — a ref string cannot say whether a signature covered its bytes — but
// here the ref IS the row's statement of where the content came from, and the
// reads that would carry it are pinned separately
// (TestEffectiveTrust_UnsetPostureWithholds, and the reader tests in
// internal/bundles).
func postureCtxOf(ref trust.Ref) bundles.TrustCtx {
	if ref.IsBuiltin || ref.IsCompanion || ref.IsLocal {
		return bundles.TrustCtxLocal
	}
	return bundles.TrustCtxRemote
}

// postureProvOf is postureCtxOf's provenance half — see its doc.
func postureProvOf(ref trust.Ref) bundles.ProvenanceClass {
	switch {
	case ref.IsBuiltin:
		return bundles.ProvenanceBuiltin
	case ref.IsCompanion:
		return bundles.ProvenanceCompanion
	case ref.IsLocal:
		return bundles.ProvenanceProject
	default:
		return bundles.ProvenanceRemote
	}
}
