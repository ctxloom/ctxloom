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
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// SignerDecision is what a TrustRoot says about one key in one namespace.
// It is core-owned so the allowedsigners adapter returns it and composite
// imports no signing package.
type SignerDecision struct {
	// Trusted is whether the key may sign in the namespace, now.
	Trusted bool
	// Principal is the identity the granting entry names; empty when untrusted.
	Principal string
	// Reason is display-only: why the key was refused, when it was.
	Reason string
}

// The three PORTS the gate decides with. signing/allowedsigners implements
// TrustRoot, signing/countersign implements ReviewRecords, and remote's
// lockfile implements RetractionRecords; config.Sources.TrustPorts builds
// all three for a generation.
type (
	// TrustRoot says which keys may publish in which namespace.
	TrustRoot interface {
		TrustedForNamespace(key ssh.PublicKey, ns string, now time.Time) SignerDecision
	}
	// ReviewRecords is what a human decided: a rejection covering the ref OR
	// exactly these bytes (both scopes must be honoured, for every item), and
	// an approval of exactly these bytes at this ref in this layout form.
	ReviewRecords interface {
		Rejected(ref trust.Ref, payload []byte) bool
		Approved(ref trust.Ref, payload []byte, form bundles.ContentForm) bool
	}
	// RetractionRecords is the LOCAL record of publisher retractions, written
	// at pull time and read here, so the decision never touches the network.
	RetractionRecords interface {
		Retracted(ref trust.Ref) (retracted bool, reason string)
	}
)

// Faulted is the OPTIONAL capability a records port exposes when its backing
// store could not be read. The gate checks it by type assertion, never as a
// port method, so a closure-shaped test fake stays two lines and a real
// disk-backed adapter fails CLOSED: "could not evaluate" never means "allow".
// A faulted ReviewRecords withholds every item; a faulted RetractionRecords
// withholds every item a retraction could cover.
type Faulted interface {
	Fault() error
}

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

// NewTrust holds the three ports and decides with them. The cascade consults
// no clock of its own: validity windows are the ports' concern, asked at
// their own call sites.
func NewTrust(root TrustRoot, records ReviewRecords, retraction RetractionRecords) (Trust, error) {
	if root == nil || records == nil || retraction == nil {
		return Trust{}, ErrTrustPortMissing
	}
	return Trust{gate: &authorizer{root: root, records: records, retraction: retraction}}, nil
}

// Ungated is the ONLY way to obtain a Trust that admits everything, and it
// is opted into BY NAME at the listing and review surfaces that must show
// pending content to a human. The default everywhere else is WITHHOLD.
func Ungated() Trust { return Trust{gate: &authorizer{ungated: true}} }

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

// Gates reports whether this Trust will actually decide anything: false only
// for Ungated. A zero Trust gates — its nil authorizer stays on the deciding
// path and is withheld there.
func (t Trust) Gates() bool { return t.gate == nil || !t.gate.ungated }

// Root is the generation's trust root, for the surfaces that verify a
// signature themselves (the readers, companion admission).
func (t Trust) Root() TrustRoot {
	if t.gate == nil {
		return nil
	}
	return t.gate.root
}

// Withheld returns the refs this gate withheld, deduplicated and sorted, so
// a surface can report WHY each was withheld — a withhold is never silent.
func (t Trust) Withheld() []string {
	if t.gate == nil {
		return nil
	}
	return t.gate.withheldRefs()
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
	ungated    bool

	withheldMu sync.Mutex
	withheld   map[string]bundles.Verdict
}

// Ungated implements the capability bundles.Gates keys on: true only for the
// authorizer Ungated() yields.
func (a *authorizer) Ungated() bool { return a.ungated }

// Admit implements bundles.Authorizer. The order is the cascade's and it is
// load-bearing: a human's rejection outranks every allow, a publisher's
// retraction outranks every allow, locality answers for content the human
// already controls, a trusted publisher's signature answers for what
// travelled, a human's approval answers for what was reviewed — and an
// executable nothing justified is WITHHELD until a review record approves it.
func (a *authorizer) Admit(e bundles.Exposure) bundles.Verdict {
	if a.ungated {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonUngated}
	}
	if !e.Read.Claimed() {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonUnestablished,
			Detail: "no reader established this bundle's provenance"})
	}
	if err := fault(a.records); err != nil {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonPending,
			Detail: "the approvals store could not be read, so nothing is approved: " + err.Error()})
	}
	if a.records.Rejected(e.Ref, e.Bytes) {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonRejected})
	}
	if retractable(e.Ref) {
		if err := fault(a.retraction); err != nil {
			return a.record(e, bundles.Verdict{Reason: bundles.ReasonPending,
				Detail: "retraction state could not be established, so nothing that travelled is trusted: " + err.Error()})
		}
	}
	// Every ref is asked about, not only the retractable ones: the port scopes
	// its own answer (a local ref has no lockfile entry), and a record that
	// DOES answer for one outranks the locality below — retraction sits above
	// every allow.
	if retracted, why := a.retraction.Retracted(e.Ref); retracted {
		return a.record(e, bundles.Verdict{Reason: bundles.ReasonRetracted, Detail: why})
	}
	if reason, ok := localReason(e.Read); ok {
		return bundles.Verdict{Allow: true, Reason: reason, Detail: admitDetail(e.Read)}
	}
	if signer := e.Read.Bundle.Signer(); signer != "" && signer != trust.BuiltinSigner {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonTrustedSigner}
	}
	if a.records.Approved(e.Ref, e.Bytes, e.Form) {
		return bundles.Verdict{Allow: true, Reason: bundles.ReasonApproved}
	}
	return a.record(e, bundles.Verdict{Reason: pendingReason(e.Read), Detail: pendingDetail(e.Ref)})
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
// withhold it.
func retractable(ref trust.Ref) bool {
	return !ref.IsLocal && !ref.IsBuiltin && ref.RepoURL != ""
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
	case bundles.ProvenanceBuiltin:
		return bundles.ReasonBuiltin, true
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

func (a *authorizer) withheldRefs() []string {
	a.withheldMu.Lock()
	defer a.withheldMu.Unlock()
	if len(a.withheld) == 0 {
		return nil
	}
	out := make([]string, 0, len(a.withheld))
	for ref := range a.withheld {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
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
func WithheldBy(auth bundles.Authorizer) []WithheldItem {
	a, ok := auth.(*authorizer)
	if !ok {
		return nil
	}
	a.withheldMu.Lock()
	defer a.withheldMu.Unlock()
	out := make([]WithheldItem, 0, len(a.withheld))
	for ref, v := range a.withheld {
		out = append(out, WithheldItem{Ref: ref, Verdict: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}
