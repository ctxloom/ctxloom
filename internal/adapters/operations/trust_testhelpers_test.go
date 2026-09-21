package operations

import (
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// The decision cascade lives in composite (composite.NewTrust); these are
// this package's TEST doubles for driving that one gate through the shapes
// the tests here were written against: a request of facts (EffectiveTrust),
// and a gate a test builds over records it injected (contentGate). Nothing
// here decides — every verdict is the production cascade's.

// ReviewRecords and RetractionRecords are the ports a test fakes.
type (
	ReviewRecords     = composite.ReviewRecords
	RetractionRecords = composite.RetractionRecords
)

// contentGate is a gate over cfg's trust root and lockfile, with the review
// records (and retraction records) a test injected; nil means the production
// adapters, built from cfg.
type contentGate struct {
	cfg        *config.Config
	records    ReviewRecords
	retraction RetractionRecords
	fs         afero.Fs

	once sync.Once
	tr   composite.Trust
}

func (g *contentGate) trust() composite.Trust {
	g.once.Do(func() {
		records := g.records
		if records == nil {
			records = buildCountersignRecords(g.cfg, g.fs, nil, nil, nil)
		}
		retraction := g.retraction
		if retraction == nil {
			retraction = remote.NewLockfileRetraction(remote.NewLockfileManager(ProjectAppDir(g.cfg), remote.WithLockfileFS(getFS(g.fs))))
		}
		tr, err := composite.NewTrust(reviewTrustRoot(g.cfg, nil), records, retraction)
		if err != nil {
			panic(err)
		}
		g.tr = tr
	})
	return g.tr
}

// Authorizer is the gate's authorizer: the real one, so a tally read back
// through composite.WithheldBy is the gate's own.
func (g *contentGate) Authorizer() bundles.Authorizer { return g.trust().Authorizer() }

func (g *contentGate) Admit(e bundles.Exposure) bundles.Verdict { return g.Authorizer().Admit(e) }

func (g *contentGate) Unaddressable(ref string, v bundles.Verdict) {
	if r, ok := g.Authorizer().(bundles.UnaddressableReporter); ok {
		r.Unaddressable(ref, v)
	}
}

func (g *contentGate) withheldItems() []composite.WithheldItem {
	return composite.WithheldBy(g.Authorizer())
}

func (g *contentGate) withheldRefs() []string { return g.trust().Withheld() }

// ExecutableTrustGate is the executable-surface view of a contentGate, as the
// tests here spell it; NewExecutableTrustGate builds one over cfg's
// production adapters.
type ExecutableTrustGate struct{ gate *contentGate }

func NewExecutableTrustGate(cfg *config.Config) *ExecutableTrustGate {
	return &ExecutableTrustGate{gate: &contentGate{cfg: cfg, fs: cfgFS(cfg)}}
}

func (e *ExecutableTrustGate) Authorizer() bundles.Authorizer { return e.gate.Authorizer() }

func (e *ExecutableTrustGate) Trust() composite.Trust { return e.gate.trust() }

func (e *ExecutableTrustGate) WarnWithheld() { WarnWithheldBy(e.gate.Authorizer()) }

// EffectiveTrustRequest carries the FACTS one decision keys on, as the
// cascade's tests state them: the item, its exact payload bytes and layout
// form, the verified signer, the posture and provenance a reader would have
// established, and the records to decide with (nil: cfg's own).
type EffectiveTrustRequest struct {
	Ref        trust.Ref
	Payload    []byte
	Form       string
	Signer     string
	Posture    bundles.TrustCtx
	Provenance bundles.ProvenanceClass
	Records    ReviewRecords
	Retraction RetractionRecords
	FS         afero.Fs
}

// EffectiveTrust decides req with the one cascade, over a read synthesised
// from req's facts. A request that states NO posture is decided as content
// that travelled: unset is not "local", so nothing but a review record or a
// trusted signer admits it, while a rejection or retraction still answers
// first — the request path's contract, which the authorizer's unclaimed-read
// withhold (composite's own test) sits above.
func EffectiveTrust(cfg *config.Config, req EffectiveTrustRequest) (*EffectiveTrustResult, error) {
	g := &contentGate{cfg: cfg, records: req.Records, retraction: req.Retraction, fs: req.FS}
	v := g.Admit(bundles.Exposure{
		Read:   readOfFacts(req),
		Ref:    req.Ref,
		RefStr: req.Ref.Key(),
		Bytes:  req.Payload,
		Form:   bundles.ContentForm(req.Form),
	})
	res := resultOf(v)
	return &res, nil
}

// readOfFacts is the read a reader would have established for req; an
// unstated posture or provenance is read as travelled content.
func readOfFacts(req EffectiveTrustRequest) bundles.BundleRead {
	posture, prov := req.Posture, req.Provenance
	if posture == bundles.TrustCtxUnset {
		posture = bundles.TrustCtxRemote
	}
	if prov == bundles.ProvenanceUnset {
		prov = bundles.ProvenanceRemote
	}
	b := &bundles.Bundle{Name: req.Ref.Bundle}
	facts := bundles.SignatureFacts{Signature: bundles.SignatureNone, Signer: bundles.SignerNone}
	if req.Signer != "" {
		facts = bundles.SignatureFacts{Signature: bundles.SignatureValid, Signer: bundles.SignerTrusted, Principal: req.Signer}
		b.StampSigner(req.Signer)
	}
	name := req.Ref.Bundle
	if name == "" {
		name = "fixture"
	}
	return bundles.NewRead(name, b, prov, posture, facts)
}
