package operations

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// testAuthorizer is the two-valued Authorizer a test reaches for when it is exercising
// what happens AROUND a decision rather than the decision itself: admit
// everything, or withhold everything. The Reasons are the plainest true ones —
// a blanket admit is not claiming a provenance it checked, and a blanket
// withhold is the ordinary pending state.
func testAuthorizer(admit bool) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Admit: true, Reason: bundles.ReasonLocal}
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
	return bundles.Decide(g, read, ref, payload, bundles.ContentForm(form)).Admit
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
