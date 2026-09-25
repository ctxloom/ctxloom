package bundles

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
)

// A real gate withholds what it refuses, and a missing gate does NOT admit.
// These tests hold the outcomes apart by the EFFECT each has on content.
// There is no production authorizer that admits everything; the allow-all
// case below uses the test-only double.

// sentinelSeed is one local bundle holding one fragment, so every assertion
// below is about whether that one body reaches the caller.
func sentinelSeed() map[string]*Bundle {
	return map[string]*Bundle{
		"demo": {Name: "demo", Fragments: map[string]BundleFragment{"secret": {
			ItemBody: ItemBody{
				Content: "secret body",
			},
		}}},
	}
}

func sentinelPipe(a Authorizer) *Pipeline {
	return NewPipeline(NewLoader(seedLocal(sentinelSeed())).WithReporter(ledger()), a, LinksUnchecked(), true)
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

// TestExposure_AdmitAll_Admits proves the admitting direction: an authorizer
// that admits serves the body, and records nothing withheld.
func TestExposure_AdmitAll_Admits(t *testing.T) {
	p := sentinelPipe(admitAllForTest())

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

	v := Decide(report.Reporter{}, nil, BundleRead{}, "demo#fragments/secret", []byte("secret body"), FormRaw)
	if v.Allow {
		t.Fatal("Decide(report.Reporter{}, nil, ...) admitted")
	}
	if v.Reason != ReasonUngoverned {
		t.Errorf("Reason = %v, want ReasonUngoverned", v.Reason)
	}
}

// TestDecide_AdmitAll_StillParsesTheRef pins that no authorizer answers
// above the ref parse: an unaddressable ref withholds even when the
// authorizer would admit anything, because nothing can key a decision on it.
func TestDecide_AdmitAll_StillParsesTheRef(t *testing.T) {
	restore := clidiag.SetSink(&bytes.Buffer{})
	defer restore()

	v := Decide(report.Reporter{}, admitAllForTest(), BundleRead{}, "not a parseable ref at all", nil, FormRaw)
	if v.Allow {
		t.Fatal("an allow-all authorizer admitted an unaddressable ref")
	}
	if v.Reason != ReasonUnaddressable {
		t.Errorf("Reason = %v, want ReasonUnaddressable", v.Reason)
	}
}
