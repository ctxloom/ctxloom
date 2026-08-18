package bundles

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// "Deliberately ungated" and "the gate was forgotten" are two different
// statements, and until AdmitAll they were spelled the same way: a nil
// Authorizer. Both admitted. These tests hold the three outcomes apart —
// a real gate withholds, AdmitAll admits, and a missing gate does NOT admit —
// by the EFFECT each has on content, not by the spelling at the call site.

// sentinelSeed is one local bundle holding one fragment, so every assertion
// below is about whether that one body reaches the caller.
func sentinelSeed() map[string]*Bundle {
	return map[string]*Bundle{
		"demo": {Name: "demo", Fragments: map[string]BundleFragment{"secret": {Content: "secret body"}}},
	}
}

func sentinelPipe(a Authorizer) *Pipeline {
	return NewPipeline(NewLoader(seedLocal(sentinelSeed())), a, true)
}

// TestExposure_RealGate_WithholdsWhatItRefuses proves the gated direction: a
// surface carrying a real authorizer serves nothing the authorizer refuses, and
// the refusal is tallied.
func TestExposure_RealGate_WithholdsWhatItRefuses(t *testing.T) {
	p := sentinelPipe(blockingGate(nil, "#fragments/secret"))

	if _, err := p.GetFragment("demo#fragments/secret"); !errors.Is(err, errs.ErrFragmentWithheld) {
		t.Fatalf("GetFragment err = %v, want ErrFragmentWithheld", err)
	}
	if w := p.Withheld(); len(w) != 1 || w[0] != "ctxloom+local:demo#fragments/secret" {
		t.Errorf("Withheld() = %v, want [ctxloom+local:demo#fragments/secret] (the canonical bundle-reference grammar)", w)
	}
}

// TestExposure_AdmitAll_Admits proves the ungated direction: a surface that
// says AdmitAll out loud serves the body, and records nothing withheld.
func TestExposure_AdmitAll_Admits(t *testing.T) {
	p := sentinelPipe(AdmitAll())

	got, err := p.GetFragment("demo#fragments/secret")
	if err != nil {
		t.Fatalf("AdmitAll GetFragment: %v", err)
	}
	if got.Content != "secret body" {
		t.Errorf("content = %q, want %q", got.Content, "secret body")
	}
	if w := p.Withheld(); len(w) != 0 {
		t.Errorf("Withheld() = %v, want empty", w)
	}
}

// TestExposure_ForgottenGate_DoesNotAdmit is the whole point: a surface that
// reached the delivery stage with NO authorizer withholds its content and says
// so. A nil authorizer is a defect in the caller, never a policy, so it may not
// resolve to the permissive answer.
func TestExposure_ForgottenGate_DoesNotAdmit(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	p := sentinelPipe(nil)

	if _, err := p.GetFragment("demo#fragments/secret"); !errors.Is(err, errs.ErrFragmentWithheld) {
		t.Fatalf("a pipeline nobody gave an authorizer served content (err = %v)", err)
	}
	if w := p.Withheld(); len(w) != 1 || w[0] != "ctxloom+local:demo#fragments/secret" {
		t.Errorf("Withheld() = %v, want [ctxloom+local:demo#fragments/secret] (the canonical bundle-reference grammar)", w)
	}
	if !strings.Contains(sink.String(), "no authorizer") {
		t.Errorf("the missing authorizer was not reported; diagnostics = %q", sink.String())
	}
}

// TestDecide_NilAuthorizer_Withholds pins the seam itself, below any pipeline:
// Decide with no authorizer is a withhold carrying ReasonUngoverned, which
// names the fault rather than blaming the content.
func TestDecide_NilAuthorizer_Withholds(t *testing.T) {
	restore := clidiag.SetSink(&bytes.Buffer{})
	defer restore()

	v := Decide(nil, BundleRead{}, "demo#fragments/secret", []byte("secret body"), FormRaw)
	if v.Allow {
		t.Fatal("Decide(nil, ...) admitted")
	}
	if v.Reason != ReasonUngoverned {
		t.Errorf("Reason = %v, want ReasonUngoverned", v.Reason)
	}
}

// TestDecide_AdmitAll_AdmitsBeforeParsing pins AdmitAll's parity with the nil
// it replaces: it answers ABOVE ref parsing, so an unaddressable ref on an
// ungated surface admits exactly as it always did. Parsing first would turn
// every management/listing path into a new withhold.
func TestDecide_AdmitAll_AdmitsBeforeParsing(t *testing.T) {
	restore := clidiag.SetSink(&bytes.Buffer{})
	defer restore()

	v := Decide(AdmitAll(), BundleRead{}, "not a parseable ref at all", nil, FormRaw)
	if !v.Allow {
		t.Fatalf("AdmitAll withheld an unaddressable ref (Reason %v)", v.Reason)
	}
	if v.Reason != ReasonUngated {
		t.Errorf("Reason = %v, want ReasonUngated", v.Reason)
	}
}

// TestGates_SeparatesDeliberateFromForgotten pins the predicate the gating
// branches key on: only AdmitAll skips a gate. A nil authorizer is NOT a skip —
// it falls into Decide, which withholds.
func TestGates_SeparatesDeliberateFromForgotten(t *testing.T) {
	if Gates(AdmitAll()) {
		t.Error("Gates(AdmitAll()) = true, want false — AdmitAll decides nothing")
	}
	if !Gates(nil) {
		t.Error("Gates(nil) = false — a forgotten authorizer would skip the gate silently")
	}
	if !Gates(blockingGate(nil)) {
		t.Error("Gates(real authorizer) = false, want true")
	}
}
