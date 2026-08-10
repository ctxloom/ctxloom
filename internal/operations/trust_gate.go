package operations

import (
	"sort"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// contentGate is the per-item trust gate bound into a bundle loader's content
// choke (bundles.fragmentContent/commandContent). It owns a review-records store
// built ONCE, so no item re-reads the countersignature stores, and resolves each
// item fail-closed: any parse/evaluation error withholds (returns false),
// matching the security mandate that "couldn't evaluate" must never mean
// "allow".
//
// That sharing covers APPROVALS ONLY. The retraction record is not shared and
// is re-read per item: retraction is left nil below (the production default),
// so EffectiveTrust builds it from the active lockfile on every call, opening
// and YAML-parsing lock.yaml once per gated item. Hoisting it would change WHEN
// retraction state is sampled, which is a trust decision rather than a caching
// one; measured and pinned in trust_perkitem_io_test.go.
//
// It receives the exact BYTES about to be exposed (pre-mustache) rather than a
// precomputed hash, so the decision can verify rather than merely compare —
// substitution only injects project-authored profile vars and cannot smuggle
// remote content past the gate.
type contentGate struct {
	cfg     *config.Config
	records ReviewRecords
	// retraction is a test-injection seam for the RETRACTION step (mirroring
	// records above): nil (the production default — buildContentGate never
	// sets it) lets
	// EffectiveTrust build its own default (the active lockfile, see
	// buildLockfileRetraction). Tests construct a *contentGate literal
	// directly (see gatedAcmeLoader, trust_exec_gate_test.go) and set this to
	// a fakeRetraction to exercise the retracted Source without touching a
	// real lockfile.
	retraction RetractionRecords
	fs         afero.Fs

	// withheld records every ref this gate denied, mapped to the FULL decided
	// result (Source + Detail) so the advisory can report WHY, not just THAT,
	// an item was withheld — a withhold must never be silent or reasonless
	// (docs/trust-model.md). The content loader surfaces its own withheld set
	// (Pipeline.Withheld(), refs only), but the executable surfaces (MCP
	// servers, bundle hooks, prompt exports) call the gate directly with no
	// loader to tally them, so the gate keeps its own record and the caller
	// surfaces a content-free, reasoned advisory (ExecutableTrustGate.
	// WarnWithheld, warnWithheld).
	withheldMu sync.Mutex
	withheld   map[string]bundles.Verdict
}

// Admit implements bundles.Authorizer: the typed façade over the decision cascade.
//
// It WRAPS EffectiveTrust and does not replace it. The cascade's step order and
// its outcomes are exactly what they were; what this adds is the two things the
// cascade structurally cannot see, both of them read FACTS rather than steps:
//
//  1. TAMPER. Remote bytes whose signature does not cover them are withheld —
//     the decision table's `remote | invalid | *` rows. This is applied AFTER
//     the cascade so rejection and retraction still answer first (both withhold
//     either way, and "rejected" is the more specific, actionable answer a user
//     acts on). It overrides an ALLOW, which is the case that matters: an item
//     a human once approved is not un-tampered by that approval, and letting a
//     corrupted `.sig` demote signed content to merely-reviewable content is the
//     spec §10.2 downgrade this row exists to close.
//  2. WHY it is pending. EffectiveTrust resolves unsigned content and
//     signed-by-an-untrusted-key content to the same SourcePending, correctly —
//     both withhold. But they are different diagnoses with different fixes
//     (`ctxloom review` vs `signer trust`), and the surface whose job is
//     helping a human decide must not collapse them. The read's Signature/Signer
//     axes are what separate them, and they never reached the old bool gate.
//
// It is a PURE FUNCTION. The stale-local-signature row admits and returns its
// warning in Verdict.Detail; nothing here emits (see bundles.ReportVerdict).
//
// FAIL-CLOSED throughout: an unclaimed read, an evaluation error, or a nil
// result all withhold.
func (g *contentGate) Admit(e bundles.Exposure) bundles.Verdict {
	if !e.Read.Claimed() {
		// A read that established nothing is not content of unknown trust, it is
		// a value nobody spoke for. Withhold rather than let a zero value read
		// as "local, unsigned, no signer".
		return g.record(e, bundles.Verdict{Reason: bundles.ReasonUnestablished,
			Detail: "no reader established this bundle's provenance"})
	}
	res, err := EffectiveTrust(g.cfg, EffectiveTrustRequest{
		Ref:        e.Ref,
		Payload:    e.Bytes,
		Form:       string(e.Form),
		Signer:     e.Read.Bundle.Signer(),
		Posture:    e.Read.TrustCtx(),
		Provenance: e.Read.Provenance,
		Records:    g.records,
		Retraction: g.retraction,
		FS:         g.fs,
	})
	if err != nil || res == nil {
		return g.record(e, bundles.Verdict{Reason: bundles.ReasonPending,
			Detail: evaluationErrorDetail(err)})
	}
	return g.verdictFor(e, *res)
}

// evaluationErrorDetail renders the elaboration for a decision that could not be
// evaluated at all. Never empty: "withheld, and I cannot tell you why" is the
// one thing a withhold may never say.
func evaluationErrorDetail(err error) string {
	if err != nil {
		return "the trust decision could not be evaluated: " + err.Error()
	}
	return "the trust decision returned no result"
}

// verdictFor turns a decided EffectiveTrustResult into the Verdict every surface
// renders, applying the two read-fact refinements Admit documents.
func (g *contentGate) verdictFor(e bundles.Exposure, res EffectiveTrustResult) bundles.Verdict {
	remoteTamper := e.Read.TrustCtx() == bundles.TrustCtxRemote && e.Read.Signature() == bundles.SignatureInvalid

	switch res.Source {
	case trust.SourceRejected:
		// Step 1, above everything including tamper: a human's own decision is
		// the answer they need to see.
		return g.record(e, bundles.Verdict{Reason: bundles.ReasonRejected})
	case trust.SourceRetracted:
		return g.record(e, bundles.Verdict{Reason: bundles.ReasonRetracted, Detail: res.Detail})
	}
	if remoteTamper {
		return g.record(e, bundles.Verdict{Reason: bundles.ReasonTampered, Detail: e.Read.SignatureDetail()})
	}
	if res.Trusted() {
		return bundles.Verdict{Allow: true, Reason: admitReason(e, res.Source), Detail: admitDetail(e)}
	}
	return g.record(e, bundles.Verdict{Reason: pendingReason(e.Read)})
}

// admitReason names WHICH rule allowed an exposure. The stale-local-signature
// row is an ALLOW with something to say, so it wins the naming over the plain
// first-party sources it is layered on: the author is told once, at the moment
// their content stopped being publishable, instead of at publish time.
func admitReason(e bundles.Exposure, source trust.Source) bundles.Reason {
	if staleLocalSignature(e.Read) {
		return bundles.ReasonStaleLocalSignature
	}
	switch source {
	case trust.SourceLocal:
		return bundles.ReasonLocal
	case trust.SourceBuiltin:
		return bundles.ReasonBuiltin
	case trust.SourceCompanion:
		return bundles.ReasonCompanion
	case trust.SourceTrustedSigner:
		return bundles.ReasonTrustedSigner
	default:
		return bundles.ReasonApproved
	}
}

// admitDetail is the sentence an admit-with-warning carries; empty for a plain
// allow, which has nothing to tell anyone.
func admitDetail(e bundles.Exposure) string {
	if staleLocalSignature(e.Read) {
		return bundles.StaleSignatureAdvice(e.Read)
	}
	return ""
}

// staleLocalSignature reports the decision table's `local | invalid | *` row: a
// signature over LOCAL bytes that no longer covers them. Almost always an author
// who edited and did not re-sign, which is why it warns rather than withholds —
// locality already answered the trust question and there is nothing to gate.
func staleLocalSignature(read bundles.BundleRead) bool {
	return read.TrustCtx() == bundles.TrustCtxLocal && read.Signature() == bundles.SignatureInvalid
}

// pendingReason says WHY nothing justified exposure, using the read facts the
// cascade collapsed into one SourcePending.
//
// Only REMOTE content gets the finer answer. A local-posture item that reached
// here was denied by something other than its provenance — an unreadable
// approvals store is the reachable case — and telling that user "unsigned" would
// name a fact that has nothing to do with why their content is missing.
func pendingReason(read bundles.BundleRead) bundles.Reason {
	if read.TrustCtx() != bundles.TrustCtxRemote {
		return bundles.ReasonPending
	}
	switch {
	case read.Signature() == bundles.SignatureNone:
		return bundles.ReasonUnsigned
	case read.Signer() == bundles.SignerUntrusted:
		return bundles.ReasonUntrustedSigner
	default:
		return bundles.ReasonPending
	}
}

// record tallies a withheld exposure with the FULL verdict — Reason plus any
// Detail — so a later advisory can name why, not just that, it was withheld
// (deduplicated, lazily allocated). It returns the verdict so every withholding
// arm above is one line and none of them can forget to record.
func (g *contentGate) record(e bundles.Exposure, v bundles.Verdict) bundles.Verdict {
	g.withheldMu.Lock()
	if g.withheld == nil {
		g.withheld = make(map[string]bundles.Verdict)
	}
	g.withheld[e.Ref.ItemRef()] = v
	g.withheldMu.Unlock()
	return v
}

// Unaddressable implements bundles.UnaddressableReporter: it records a ref
// nothing could parse under that ref VERBATIM, because the string the caller
// used is the only identity such an item has. A withhold nothing recorded is a
// withhold nothing can report.
func (g *contentGate) Unaddressable(ref string, v bundles.Verdict) {
	g.withheldMu.Lock()
	if g.withheld == nil {
		g.withheld = make(map[string]bundles.Verdict)
	}
	g.withheld[ref] = v
	g.withheldMu.Unlock()
}

// withheldRefs returns the refs this gate withheld, deduplicated and sorted.
func (g *contentGate) withheldRefs() []string {
	g.withheldMu.Lock()
	defer g.withheldMu.Unlock()
	if len(g.withheld) == 0 {
		return nil
	}
	out := make([]string, 0, len(g.withheld))
	for ref := range g.withheld {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// withheldItem pairs a withheld ref with the full Verdict that withheld it —
// enough for a caller to print a content-free line naming both the item and WHY
// (via bundles.Reason.Explain).
type withheldItem struct {
	Ref     string
	Verdict bundles.Verdict
}

// withheldItems returns every ref this gate withheld, paired with its verdict,
// sorted by ref for stable output.
func (g *contentGate) withheldItems() []withheldItem {
	g.withheldMu.Lock()
	defer g.withheldMu.Unlock()
	if len(g.withheld) == 0 {
		return nil
	}
	out := make([]withheldItem, 0, len(g.withheld))
	for ref, v := range g.withheld {
		out = append(out, withheldItem{Ref: ref, Verdict: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// buildContentGate constructs the *contentGate every real caller uses
// (exposurePipelineGated, NewExecutableTrustGate). It is the shared builder.
// records/fs are injection points for testing; production passes nil records
// and they are built from cfg (OS fs unless cfg carries an injected one).
// Returning the struct (not just g.allow) lets callers read the withheld
// tally afterward.
func buildContentGate(cfg *config.Config, records ReviewRecords, fs afero.Fs) *contentGate {
	g := &contentGate{cfg: cfg, fs: fs}
	if records == nil {
		records = newCountersignRecords(cfg, fs)
	}
	g.records = records
	return g
}

// ExecutableTrustGate gates the bundle EXECUTABLE surfaces — MCP servers and
// bundle hooks written to backend settings, plus prompt command-file exports —
// through the same per-item decision function as content. These surfaces bypass
// the content loader (they resolve via config.ResolveBundle* → WriteSettings,
// and backends.LoadCommandExports), so a rejection at the loader would be a
// no-op; each is gated at its OWN choke. The gate is injected into
// config.SetExecutableTrustGate (consulted by ResolveBundleMCPServers /
// ResolveBundleHooks / LoadCommandExports); a DENY omits the executable from
// settings (fail-closed). It tallies withheld refs so the caller can surface a
// content-free advisory.
//
// Construct ONCE per apply/run (it builds the review-records store up front).
// A nil *ExecutableTrustGate is a no-op: Authorizer() returns bundles.AdmitAll,
// which states the absence of gating rather than leaving a nil to travel.
type ExecutableTrustGate struct {
	gate *contentGate
}

// NewExecutableTrustGate builds the executable gate for cfg (production passes
// the OS fs via cfg).
func NewExecutableTrustGate(cfg *config.Config) *ExecutableTrustGate {
	return &ExecutableTrustGate{gate: buildContentGate(cfg, nil, cfgFS(cfg))}
}

// Authorizer returns the bundles.Authorizer the resolvers/loaders consult, or
// bundles.AdmitAll (deliberately no gating) for a nil receiver/gate.
func (e *ExecutableTrustGate) Authorizer() bundles.Authorizer {
	if e == nil || e.gate == nil {
		return bundles.AdmitAll()
	}
	return e.gate
}

// WarnWithheld surfaces one content-free advisory line PER bundle executable
// this gate withheld (MCP servers, hooks, prompt exports), naming the item and
// WHY — rejected, retracted by the publisher, or pending review — never a
// bare "withheld" (docs/trust-model.md: a withhold must never be silent or
// reasonless). Purely advisory (fault tolerance); a no-op when nothing was
// withheld.
func (e *ExecutableTrustGate) WarnWithheld() {
	if e == nil || e.gate == nil {
		return
	}
	warnWithheldItems(e.gate.withheldItems())
}

// exposurePipeline returns the exposure PROCESS stage: one bundle reader — the
// same cfg.BundleLoader every management and listing path uses — wrapped
// in a pipeline that gates with the content trust gate and serves the
// configured form. ONLY exposure surfaces build it (assembly, the ctxloom://
// fragment|prompt|skill resources, fragment-reading hooks, SessionStart regen);
// management/listing paths keep reading through the bare loader, which resolves
// pending content so a human can still review, accept or stamp it.
//
// "Is this exposure gated" is therefore a property of the pipeline, not of the
// reader — nothing about how the reader was CONSTRUCTED differs between the two.
// cfg.FS() is threaded so the gate's review-records store reads the same
// filesystem as the rest of the operation (OS fs in production, a virtualized
// fs in tests).
func exposurePipeline(cfg *config.Config, opts ...config.BundleLoaderOption) *bundles.Pipeline {
	pipe, _ := exposurePipelineGated(cfg, opts...)
	return pipe
}

// exposurePipelineGated is exposurePipeline's sibling: it builds the identical
// gated exposure pipeline but ALSO returns the underlying *contentGate, so a
// caller that reports why items were withheld (warnWithheld) can read each
// withheld ref's full decided result — Source and Detail — instead of just a
// bare ref list. Use this over exposurePipeline whenever the caller goes on to
// call warnWithheld; callers that only load content (fragments.go, commands.go
// — a single-item resource fetch that already returns a distinct withheld
// sentinel error, errs.ErrFragmentWithheld/ErrCommandWithheld) have no reasoned
// advisory to print and keep using the simpler exposurePipeline.
func exposurePipelineGated(cfg *config.Config, opts ...config.BundleLoaderOption) (*bundles.Pipeline, *contentGate) {
	gate := buildContentGate(cfg, nil, cfgFS(cfg))
	return bundles.NewPipeline(cfg.BundleLoader(opts...), gate, cfgPreferDistilled(cfg)), gate
}

// cfgPreferDistilled returns the caller's raw-vs-distilled form choice, nil-safe.
//
// Form selection is a PROCESS-stage decision (docs/design/engine-delivery-seam.
// design.md, "ALL processing lives in the middle"): the read stage — the bundle
// reader — carries no preference at all, so the pipeline that processes its
// output names the form. A nil cfg (an injected-pipeline test) gets the same
// default an unset setting does, which is distilled.
func cfgPreferDistilled(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.ShouldUseDistilled()
}

// cfgFS returns cfg's injected filesystem (nil for the OS default), nil-safe.
func cfgFS(cfg *config.Config) afero.Fs {
	if cfg == nil {
		return nil
	}
	return cfg.FS()
}

// warnWithheldItems emits one content-free advisory line PER withheld item —
// naming the item and WHY it was withheld (rejected, retracted by the
// publisher, tampered, or pending review), via bundles.Reason.Explain — so a
// withhold can never be silent or reasonless (docs/trust-model.md). Shared by
// the content-loader path (warnWithheld) and the executable path
// (ExecutableTrustGate.WarnWithheld) so both surfaces render identical
// wording for identical sources. A no-op for an empty set.
func warnWithheldItems(items []withheldItem) {
	for _, it := range items {
		clidiag.Warn("ctxloom", "withheld %s: %s", it.Ref, it.Verdict.Reason.Explain(it.Verdict.Detail))
	}
}

// warnWithheld emits a content-free, reasoned advisory for the items the trust
// gate withheld during this assembly — one line per item, naming it and why
// (see warnWithheldItems) — pointing the user at `ctxloom review` where that is
// the action. It is purely advisory (fault tolerance: never an error) and a
// no-op when nothing was withheld.
//
// gate is the *contentGate the pipeline decides with (see
// exposurePipelineGated) — it is a SUPERSET of Pipeline.Withheld() (it also
// captures builtin-fragment gate calls, which bypass the loader's own content
// choke and so never reach Pipeline.Withheld() at all — see
// config.ResolveBuiltinBundleFragments), so this also fixes a prior gap where
// a withheld builtin fragment surfaced no advisory whatsoever. Every real
// production call site (context.go, hooks.go, tooling.go) always builds
// through exposurePipelineGated, so gate is never nil in practice (the
// nil-gate/raw-loader degrade path was deleted along with warnPendingTally,
// its sole caller: that path only ever ran under a test-injected loader, and
// two of its three advisory branches — pending>0&&rejected>0, rejected-only —
// were unreachable even there, since the degrade always passed rejected=0).
func warnWithheld(gate *contentGate) {
	if gate == nil {
		return
	}
	warnWithheldItems(gate.withheldItems())
}
