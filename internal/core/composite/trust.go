// Package composite is the gate holder's home: the Trust a config generation
// decides with. It must never know which engine, where files land, or the
// session. Today it holds the gate and the ONE decision cascade every
// exposure and executable resolver consults; selection, assembly and the
// wire form of the package arrive with the composite slice.
package composite

import (
	"errors"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// The three trust ports, their decision, and the optional Faulted capability
// are declared in core/trust (the leaf) and ALIASED here, so the gate holder
// and every reader of a signature name one interface and composite imports no
// signing package to read the answer.
type (
	SignerDecision    = trust.SignerDecision
	TrustRoot         = trust.TrustRoot
	ReviewRecords     = trust.ReviewRecords
	RetractionRecords = trust.RetractionRecords
	Faulted           = trust.Faulted
)

// ErrTrustPortMissing is NewTrust's refusal: a holder with a port missing
// would have to guess the missing answer, and the only safe guess is "no".
var ErrTrustPortMissing = errors.New("composite: every trust port is required")

// Trust is the gate holder. It is built PER CONFIG GENERATION by
// config.Sources (Snapshot.Trust), so retraction records and the lockfile
// they came from cannot outlive the generation they belong to. It has NO
// permissive zero value: a zero Trust holds no gate, and the nil authorizer
// it yields is the spelling bundles.Decide withholds on loudly.
type Trust struct {
	gate *authorizer
	// external is a gate built elsewhere (Gated); nil for a Trust built here.
	external bundles.Authorizer
}

// TrustOption adjusts the gate NewTrust builds.
type TrustOption func(*authorizer)

// WithoutSignatureCheck waives the signature step, by name: remote content
// nothing else justified because it is unsigned, or signed by a key the root
// does not trust, is admitted as bundles.ReasonSigCheckDisabled instead of
// withheld pending review.
//
// It is NOT an admit-everything gate, and that is the point of building it
// here rather than as a Trust of its own: every port is still required and
// every step above the signature step still decides first — a rejection, a
// retraction or an unreadable approvals store refuses exactly as before. The
// trust root is untouched, so the readers and companion admission verify as
// they always do.
func WithoutSignatureCheck() TrustOption {
	return func(a *authorizer) { a.sigCheckWaived = true }
}

// NewTrust holds the three ports and decides with them. The cascade consults
// no clock of its own: validity windows are the ports' concern, asked at
// their own call sites.
func NewTrust(root TrustRoot, records ReviewRecords, retraction RetractionRecords, opts ...TrustOption) (Trust, error) {
	if root == nil || records == nil || retraction == nil {
		return Trust{}, ErrTrustPortMissing
	}
	gate := &authorizer{root: root, records: records, retraction: retraction}
	for _, opt := range opts {
		opt(gate)
	}
	return Trust{gate: gate}, nil
}

// SignatureCheckDisabled reports whether this Trust was built
// WithoutSignatureCheck. False for a zero or Gated Trust.
func (t Trust) SignatureCheckDisabled() bool { return t.gate != nil && t.gate.sigCheckWaived }

// Gated is a Trust over a gate built elsewhere: the injected-stage seam,
// for a caller holding a process stage whose gate it did not build here (a
// test's pipeline over its own authorizer). It gates — Assemble accepts
// it — and it withholds through the gate it was given.
func Gated(auth bundles.Authorizer) Trust { return Trust{external: auth} }

// Authorizer is the gate the resolvers consult. Nil for a zero Trust, which
// bundles.Decide withholds on and names.
func (t Trust) Authorizer() bundles.Authorizer {
	if t.external != nil {
		return t.external
	}
	if t.gate == nil {
		return nil
	}
	return t.gate
}

// Root is the generation's trust root, for the surfaces that verify a
// signature themselves (the readers, companion admission). A Trust that holds
// no root — zero or Gated — answers with trust.NoSigners, which trusts
// no key: a signature checked against a Trust no generation built fails
// closed instead of dereferencing nil.
func (t Trust) Root() TrustRoot {
	if t.gate == nil {
		return trust.NoSigners{}
	}
	return t.gate.root
}

// authorizer is the decision cascade, as a bundles.Authorizer. It is a PURE
// FUNCTION of the exposure and the ports: nothing here emits — an admit with
// a warning returns it in Verdict.Detail and the caller says it
// (bundles.ReportVerdict). Fail-closed throughout: an unclaimed read, a
// faulted port, or nothing positively justifying exposure all withhold.
type authorizer struct {
	root       TrustRoot
	records    ReviewRecords
	retraction RetractionRecords
	// sigCheckWaived is WithoutSignatureCheck.
	sigCheckWaived bool

	withheldMu sync.Mutex
	withheld   map[string]bundles.Verdict
	// reported is the subset of withheld NewlyWithheldBy has already handed
	// out. The gate lives for the whole generation and more than one site in
	// a single command prints the advisory, so without it each site reprinted
	// every earlier withhold.
	reported map[string]bool
}

// Admit implements bundles.Authorizer. The order is the cascade's and it is
// load-bearing: a human's rejection outranks every allow, a publisher's
// retraction outranks every allow, locality answers for content the human
// already controls, a trusted publisher's signature answers for what
// travelled, a human's approval answers for what was reviewed — and an
// executable nothing justified is WITHHELD until a review record approves it.
func (a *authorizer) Admit(e bundles.Exposure) bundles.Verdict {
	if !e.Read.Claimed() {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonUnestablished,
			Detail: "no reader established this bundle's provenance"})
	}
	// Zero-equality, not IsItem: a bundle-level ref (a companion) is a real
	// identity decided here. Only the identity that was never set is refused —
	// the same unaddressable withhold Decide gives a ref it cannot parse, since
	// no rejection, approval or retraction can be keyed on no bundle.
	if e.BundleRef == (trust.BundleRef{}) {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonUnaddressable,
			Detail: "the exposure names no bundle, so no rule could be keyed on it"})
	}
	if err := fault(a.records); err != nil {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonRecordsUnreadable,
			Detail: "the approvals store could not be read, so nothing is approved: " + err.Error()})
	}
	if a.records.Rejected(e.Ref(), e.Bytes) {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonRejected})
	}
	if v, refused := a.retractionVerdict(e); refused {
		return a.record(e, v)
	}
	if reason, ok := localReason(e.Read); ok {
		return bundles.Verdict{Allow: true, Reason: reason, Detail: admitDetail(e.Read)}
	}
	if signer := e.Read.Bundle.Signer(); signer != "" {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonTrustedSigner}
	}
	if a.records.Approved(e.Ref(), e.Bytes, e.Form) {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonApproved}
	}
	return a.unjustified(e)
}

// unjustified is Admit's last step, for an exposure nothing above justified:
// withheld pending review — unless the only thing missing was a signature the
// gate was built to stop asking for (WithoutSignatureCheck).
func (a *authorizer) unjustified(e bundles.Exposure) bundles.Verdict {
	pending := pendingReason(e.Read)
	if a.sigCheckWaived && signatureDerived(pending) {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonSigCheckDisabled}
	}
	return a.record(e, bundles.Verdict{Reason: pending, Detail: pendingDetail(e.Ref())})
}

// signatureDerived reports whether a pending reason is the signature step's
// answer — the only answer WithoutSignatureCheck waives.
func signatureDerived(r bundles.Reason) bool {
	return r == bundles.ReasonUnsigned || r == bundles.ReasonUntrustedSigner
}

// retractionVerdict is Admit's retraction step: a refusal verdict and true when
// the exposure must be withheld on retraction grounds, else false.
func (a *authorizer) retractionVerdict(e bundles.Exposure) (bundles.Verdict, bool) {
	if retractable(e.Ref()) {
		if err := fault(a.retraction); err != nil {
			return bundles.Verdict{Reason: bundles.ReasonRecordsUnreadable,
				Detail: "retraction state could not be established, so nothing that travelled is trusted: " + err.Error()}, true
		}
	}
	// Every ref is asked about, not only the retractable ones: the port scopes
	// its own answer (a local ref has no lockfile entry), and a record that
	// DOES answer for one outranks the locality in Admit — retraction sits
	// above every allow.
	if retracted, why := a.retraction.Retracted(e.BundleRef); retracted {
		return bundles.Verdict{Reason: bundles.ReasonRetracted, Detail: why}, true
	}
	return bundles.Verdict{}, false
}

// fault reads the optional Faulted capability off a port.
func fault(port any) error {
	if f, ok := port.(Faulted); ok {
		return f.Fault()
	}
	return nil
}

// retractable reports whether a retraction record could cover ref — only
// content that travelled from a publisher's repository has a publisher who
// can withdraw it — and so whether an unreadable retraction record must
// withhold it. A companion loadout has no lockfile entry (its RepoURL is the
// fixed ctxloom:companion token, not a repository), so no retraction can
// cover it and an unreadable lockfile has nothing to say about it — ctxloom's
// own loadout included, which a project-less start with a broken home
// lockfile must still receive.
func retractable(ref trust.Ref) bool {
	return !ref.IsLocal && !ref.IsCompanion && ref.RepoURL != ""
}

// localReason answers for content the human already controls: authored in
// the project, compiled into the binary, or advertised by a companion binary
// that already ran as the user. The stale-local-signature row wins the naming
// over ReasonLocal because it is an allow WITH something to say.
func localReason(read bundles.BundleRead) (bundles.Reason, bool) {
	if read.TrustCtx() != bundles.TrustCtxLocal {
		return bundles.ReasonUnset, false
	}
	if staleLocalSignature(read) {
		return bundles.ReasonStaleLocalSignature, true
	}
	switch read.Provenance {
	case bundles.ProvenanceProject:
		return bundles.ReasonLocal, true
	case bundles.ProvenanceCompanion:
		return bundles.ReasonCompanion, true
	}
	return bundles.ReasonUnset, false
}

// staleLocalSignature is the decision table's `local | invalid | *` row: a
// signature over LOCAL bytes that no longer covers them. The signature is
// treated as absent, not as a refusal — locality is the trust boundary for a
// bundle the human already controls under source control; the signature is
// for what travels — and the author is told (admitDetail), because they have
// silently lost the ability to publish it as signed content.
func staleLocalSignature(read bundles.BundleRead) bool {
	return read.TrustCtx() == bundles.TrustCtxLocal && read.Signature() == bundles.SignatureInvalid
}

// admitDetail is the sentence an admit-with-warning carries; empty for a
// plain allow, which has nothing to tell anyone.
func admitDetail(read bundles.BundleRead) string {
	if staleLocalSignature(read) {
		return bundles.StaleSignatureAdvice(read)
	}
	return ""
}

// pendingReason says WHY nothing justified exposure, using the read facts.
// Only REMOTE content gets the finer answer: a local-posture item that
// reached here was denied by something other than its provenance.
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

// pendingDetail names what would admit a withheld executable — a command, a
// skill, a hook, an MCP server: a review record. A fragment is text; its
// reason already says everything.
func pendingDetail(ref trust.Ref) string {
	if ref.Kind == trust.KindFragment {
		return ""
	}
	return "no review record approves this " + string(ref.Kind)
}

// record tallies a withheld exposure with its FULL verdict, keyed on the
// canonical ref string the decision was taken under, so a later advisory can
// name why — not just that — it was withheld. It returns the verdict so every
// withholding arm above is one line and none can forget to record.
func (a *authorizer) record(e bundles.Exposure, v bundles.Verdict) bundles.Verdict {
	a.recordRef(e.RefString(), v)
	return v
}

// Unaddressable implements bundles.UnaddressableReporter: a ref nothing could
// parse is recorded under that ref VERBATIM, the only identity it has.
func (a *authorizer) Unaddressable(ref string, v bundles.Verdict) { a.recordRef(ref, v) }

func (a *authorizer) recordRef(ref string, v bundles.Verdict) {
	a.withheldMu.Lock()
	if a.withheld == nil {
		a.withheld = make(map[string]bundles.Verdict)
	}
	a.withheld[ref] = v
	a.withheldMu.Unlock()
}

// WithheldItem pairs a withheld ref with the verdict that withheld it —
// enough for a caller to print a content-free line naming both the item and
// why (bundles.Reason.Explain).
type WithheldItem struct {
	Ref     string
	Verdict bundles.Verdict
}

// WithheldBy returns every ref the authorizer auth withheld, paired with its
// verdict and sorted by ref; nil when auth is not a Trust's gate.
func WithheldBy(auth bundles.Authorizer) []WithheldItem { return withheldBy(auth, false) }

// NewlyWithheldBy is WithheldBy restricted to the refs no earlier
// NewlyWithheldBy call on auth returned, and it marks them returned: the
// source for an advisory that must name each withheld item once, however many
// sites in one command print it.
func NewlyWithheldBy(auth bundles.Authorizer) []WithheldItem { return withheldBy(auth, true) }

func withheldBy(auth bundles.Authorizer, onlyNew bool) []WithheldItem {
	a, ok := auth.(*authorizer)
	if !ok {
		return nil
	}
	a.withheldMu.Lock()
	defer a.withheldMu.Unlock()
	if onlyNew && a.reported == nil {
		a.reported = make(map[string]bool)
	}
	out := make([]WithheldItem, 0, len(a.withheld))
	for ref, v := range a.withheld {
		if onlyNew {
			if a.reported[ref] {
				continue
			}
			a.reported[ref] = true
		}
		out = append(out, WithheldItem{Ref: ref, Verdict: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}
