package operations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// mustParseProducerRef parses ref — an item ref a MIGRATED producer emitted
// (ItemRead.TrustRef, LoadedSkill.TrustRef, config's BuiltinFragment.Name, or
// a ref built by hand through trust.BundleRef.WithItem in a test) — into the
// trust.Ref shape a test wants to feed to a fixture's rejectRef/blacklist
// helper or assert IsLocal/IsBuiltin/RepoURL/Key() against.
//
// It is the TEST-SIDE analog of bundles.Decide's own step 4 switch
// (trust.ParseBundleRef + trust.RefFromBundleRef), and the counterpart to
// pipelineItemRef below: a PRODUCER emits the canonical
// "ctxloom+<class>:...#<kind>/<item>" grammar, while an ASK the pipeline
// composes is still spelled "<source>#<kind>/<item>". A test asserting
// against a producer's literal output must use this one, or its assertion is
// pinned to a grammar the producer does not speak.
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

// pipelineItemRef decomposes an item ref written in the ASSEMBLY PIPELINE's
// own spelling — "<source>#<kind>/<name>", where source is a lockfile ref, a
// "ctxloom:local@bundles/<name>" identity, or a bare local bundle name — into
// the trust.Ref that addresses it. It is the shape seedLoader and
// Catalog.Lookup still speak, so a fixture written in it needs a bridge to the
// canonical grammar; the bridge is remote.ParseReference plus the shared
// selector parser, the same two the readers themselves mint through.
func pipelineItemRef(t *testing.T, ref string) trust.Ref {
	t.Helper()
	base, sel, scoped := strings.Cut(ref, "#")
	require.True(t, scoped, "fixture ref %q must carry a #<kind>/<name> selector", ref)
	kind, name, err := trust.ParseSelector(sel)
	require.NoError(t, err, "fixture ref %q must carry a recognized selector", ref)
	parsed, perr := remote.ParseReference(base)
	if perr != nil {
		require.False(t, remote.IsSelfContainedRef(base),
			"fixture ref %q carries a scheme marker and must parse as one", ref)
		return trust.Ref{Bundle: base, Kind: kind, Name: name, IsLocal: true}
	}
	return trust.Ref{
		RepoURL: parsed.URL, Bundle: parsed.Path, Kind: kind, Name: name,
		IsLocal: parsed.IsLocal, IsCompanion: parsed.IsCompanion,
	}
}

// canonicalWithheldRef converts a pipeline-spelled item ref into the canonical
// bundle-reference string a producer emits for the IDENTICAL item, so a
// fixture can assert Pipeline.Withheld()'s literal tally without hand-deriving
// the canonical grammar's host-splitting/escaping rules a second, competing
// way. Ref.AsBundleRef is the exact bridge ItemRefFor/fragmentRead use in
// production.
func canonicalWithheldRef(t *testing.T, ref string) string {
	t.Helper()
	br, err := pipelineItemRef(t, ref).AsBundleRef()
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
