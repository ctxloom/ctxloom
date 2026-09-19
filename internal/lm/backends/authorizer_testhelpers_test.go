package backends

import (
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// The gates a test binds to a fixture Config are REAL gates
// (composite.NewTrust) over fake ports: what a test states is what a human
// decided, and the cascade the choke consults is the one production decides
// with. A profile's hooks are project-authored, so locality admits them; a
// test that wants one withheld records a REJECTION, the one thing that
// outranks locality.

// testAuthorizer is the two-valued Authorizer a test passes DIRECTLY to a
// choke that takes one: admit everything, or withhold everything as pending.
func testAuthorizer(admit bool) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(bundles.Exposure) bundles.Verdict {
		if admit {
			return bundles.Verdict{Allow: true, Reason: bundles.ReasonLocal}
		}
		return bundles.Verdict{Reason: bundles.ReasonPending}
	})
}

// recordingAuthorizer is testAuthorizer that also records every ref the
// choke fed it, for a choke that takes the authorizer directly.
func recordingAuthorizer(admit bool, gotRefs *[]string) bundles.Authorizer {
	return bundles.AuthorizerFunc(func(e bundles.Exposure) bundles.Verdict {
		*gotRefs = append(*gotRefs, e.RefString())
		return testAuthorizer(admit).Admit(e)
	})
}

// admitting is the gate a fixture binds when the test is about what happens
// AROUND a decision: no rejection, no retraction, locality admits.
func admitting() composite.Trust { return compositetest.Trust() }

// rejectingAll is the deny-everything gate: a human rejection of every item.
func rejectingAll() composite.Trust { return compositetest.Trust(compositetest.RejectAll()) }

// recordingTrust admits everything and records every ref the choke fed the
// gate, rendered as the canonical bundle-reference string.
func recordingTrust(gotRefs *[]string) composite.Trust {
	return compositetest.Trust(compositetest.Observe(func(ref trust.Ref, _ []byte) {
		br, err := ref.AsBundleRef()
		if err != nil {
			*gotRefs = append(*gotRefs, "unrenderable: "+err.Error())
			return
		}
		*gotRefs = append(*gotRefs, br.String())
	}))
}

// hashTrust admits exactly the payloads whose hash is in want — the shape a
// countersignature has: one approval covers one set of bytes — by REJECTING
// every other payload, so a project-authored hook outside the grant is
// withheld despite its locality.
func hashTrust(want ...string) composite.Trust {
	granted := make(map[string]bool, len(want))
	for _, w := range want {
		granted[w] = true
	}
	return compositetest.Trust(compositetest.RejectWhen(func(_ trust.Ref, payload []byte) bool {
		return !granted[bundles.HashPayload(payload)]
	}))
}

// gatedFixture is config.NewFixture with the admitting gate bound: a fixture
// that exercises an executable surface must state its gate, and the plain
// one is what a test about resolution rather than trust means.
func gatedFixture(f config.Fixture) *config.Config {
	cfg := config.NewFixture(f)
	cfg.BindTrustForTesting(admitting())
	return cfg
}
