package bundles

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// PER-FORM TRUST GRANTS — pinned ahead of the tree migration, because the
// migration is where they are cheapest to lose.
//
// ContentForm's own doc states the rule: a grant "blessing the raw form can
// never validate a distilled exposure, and vice-versa." A distilled body is
// typically a MODEL'S REWRITE of prose a human approved, and an unread rewrite
// must never inherit the approval of the original it was derived from.
//
// The tree migration gives every item ONE SHA256SUMS manifest covering all its
// bodies at once, and a single signature over that manifest would bless every
// body together — exactly what this forbids. These tests are the thing that
// goes red if that "simplification" is ever made, so the trust boundary
// survives the restructure as a checked fact rather than a comment.
//
// EVERY DENIAL ARM ASSERTS WITHHELD, NOT MERELY UNSELECTED. Falling back to the
// granted body would be far worse than a withhold and would satisfy any test
// that only asserted "the distilled body was not served".

// grantFor admits exactly ONE (content-hash, form) pair and denies everything
// else — which is what a countersignature over one body IS. Keying on both is
// the whole point: a grant that matched on hash alone would admit a body it
// never covered the moment two forms shared bytes.
func grantFor(hash string, form ContentForm) Authorizer {
	return authorizerFunc(func(e Exposure) Verdict {
		if HashPayload(e.Bytes) == hash && e.Form == form {
			return admitVerdict()
		}
		return denyVerdict()
	})
}

// twoFormFragment is one item with two bodies whose bytes differ, so a verdict
// can never be ambiguous about which one it saw.
func twoFormFragment() (BundleFragment, map[string]*Bundle) {
	frag := BundleFragment{
		ItemBody: ItemBody{
			Content:   "RAW-BODY-MARKER",
			Distilled: "DISTILLED-BODY-MARKER",
		},
	}
	return frag, map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": frag}}}
}

// TestPerFormGrant_RawGrantDoesNotValidateTheDistilledBody is the arm that
// matters most: the human read the authored prose, and a model rewrote it.
func TestPerFormGrant_RawGrantDoesNotValidateTheDistilledBody(t *testing.T) {
	frag, seed := twoFormFragment()
	rawHash, rawForm := frag.EffectiveContentHash(false)
	require.Equal(t, FormRaw, rawForm, "the fixture must actually produce a raw form")

	// Granted: the raw body. Requested: the distilled one.
	l := gatedPipe(NewLoader(seedLocal(seed)), grantFor(rawHash, FormRaw), true)

	got, err := l.GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a grant over the authored body must WITHHOLD the distilled body, not admit it: %v", err)
	// Withheld, not quietly downgraded to the body that WAS granted.
	if got != nil {
		assert.NotEqual(t, "RAW-BODY-MARKER", got.Content,
			"withholding must not silently fall back to the granted form")
	}
}

// TestPerFormGrant_DistilledGrantDoesNotValidateTheRawBody is the mirror. It is
// not symmetry for its own sake: an approval recorded against a compressed
// rewrite says nothing about the full text it came from, which may be longer,
// and may say things the compression dropped.
func TestPerFormGrant_DistilledGrantDoesNotValidateTheRawBody(t *testing.T) {
	frag, seed := twoFormFragment()
	distilledHash, distilledForm := frag.EffectiveContentHash(true)
	require.Equal(t, FormDistilled, distilledForm, "the fixture must actually produce a distilled form")

	// Granted: the distilled body. Requested: the raw one.
	l := gatedPipe(NewLoader(seedLocal(seed)), grantFor(distilledHash, FormDistilled), false)

	got, err := l.GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a grant over the distilled body must WITHHOLD the authored body: %v", err)
	if got != nil {
		assert.NotEqual(t, "DISTILLED-BODY-MARKER", got.Content,
			"withholding must not silently fall back to the granted form")
	}
}

// TestPerFormGrant_TheMatchingFormIsAdmitted is the positive control. Without
// it both denials above would pass against a pipeline that withheld
// everything — absence satisfying absence, which is how this project's most
// expensive vacuous tests were built.
func TestPerFormGrant_TheMatchingFormIsAdmitted(t *testing.T) {
	frag, seed := twoFormFragment()

	rawHash, _ := frag.EffectiveContentHash(false)
	got, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(rawHash, FormRaw), false).GetFragment("b#fragments/f")
	require.NoError(t, err)
	assert.Equal(t, "RAW-BODY-MARKER", got.Content, "a grant over the requested form must admit it")

	distilledHash, _ := frag.EffectiveContentHash(true)
	got, err = gatedPipe(NewLoader(seedLocal(seed)), grantFor(distilledHash, FormDistilled), true).GetFragment("b#fragments/f")
	require.NoError(t, err)
	assert.Equal(t, "DISTILLED-BODY-MARKER", got.Content)
}

// TestEmptyPremise_IsInjectedNotWithheld pins the DEFAULT INVERSION between
// fragments and skills, which the migration plan names as the easiest thing
// here to get wrong:
//
//	skill:    no match  -> withheld  (opt-in)
//	fragment: empty     -> injected  (opt-out)
//
// A fragment carrying no applicability condition is UNCONDITIONAL, not inert.
// Migrating today's premise-less fragments into a world where an empty
// predicate withholds would silently strip context from every session while
// every surface reported success — and a fragment that is quietly absent looks
// exactly like one that was never authored.
func TestEmptyPremise_IsInjectedNotWithheld(t *testing.T) {
	seed := map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{
		"unconditional": {
			ItemBody: ItemBody{
				Content: "ALWAYS-INJECTED-MARKER",
			},
		},
		"conditional": {
			ItemBody: ItemBody{
				Content: "CONDITIONAL-MARKER",
			},
			Premise: "when working on Go code",
		},
	}}}
	l := gatedPipe(NewLoader(seedLocal(seed)), blockingGate(nil), false)

	got, err := l.GetFragment("b#fragments/unconditional")
	require.NoError(t, err, "a fragment with no premise must resolve, not be withheld for lacking one")
	assert.Equal(t, "ALWAYS-INJECTED-MARKER", got.Content)
	assert.Empty(t, got.Premise, "the fixture's premise-less fragment must genuinely carry no premise")

	// The sibling proves an authored premise is CARRIED rather than dropped, so
	// the empty case above is a real default and not the only thing that works.
	cond, err := l.GetFragment("b#fragments/conditional")
	require.NoError(t, err)
	assert.Equal(t, "when working on Go code", cond.Premise)
}

// TestPerFormGrant_FormIsBoundIndependentlyOfTheBytes is the case the content
// hash CANNOT separate, and therefore the only one that proves the grant is
// keyed on {hash, FORM} rather than on the hash alone: two bodies whose bytes
// are identical.
//
// It is not a contrived shape — a distillation that changes nothing produces
// exactly it — but the reason it is pinned is structural. The tree migration
// hands every item ONE manifest covering all its bodies, so the temptation is
// to drop the form from the grant key on the grounds that the hash already
// identifies the content. It does not identify the EXPOSURE: same bytes served
// as a distilled rewrite is a different claim about provenance from the same
// bytes served as the author's own text, and only the form says which.
func TestPerFormGrant_FormIsBoundIndependentlyOfTheBytes(t *testing.T) {
	const shared = "IDENTICAL-BODY-MARKER"
	frag := BundleFragment{
		ItemBody: ItemBody{
			Content:   shared,
			Distilled: shared,
		},
	}
	seed := map[string]*Bundle{"b": {Name: "b", Fragments: map[string]BundleFragment{"f": frag}}}

	rawHash, rawForm := frag.EffectiveContentHash(false)
	distilledHash, distilledForm := frag.EffectiveContentHash(true)
	require.Equal(t, FormRaw, rawForm)
	require.Equal(t, FormDistilled, distilledForm)
	require.Equal(t, rawHash, distilledHash,
		"this test is only meaningful while the two forms hash identically — if they diverge, the hash alone would separate them and nothing here tests the form")

	// Granted: the RAW form. Requested: the DISTILLED form. The bytes match, so
	// only the form can deny this.
	_, err := gatedPipe(NewLoader(seedLocal(seed)), grantFor(rawHash, FormRaw), true).GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"identical bytes must not let a raw grant validate a distilled exposure: %v", err)

	// And the mirror, so neither direction is the only one wired.
	_, err = gatedPipe(NewLoader(seedLocal(seed)), grantFor(distilledHash, FormDistilled), false).GetFragment("b#fragments/f")
	require.Error(t, err)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"identical bytes must not let a distilled grant validate a raw exposure: %v", err)
}
