package bundles

import (
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The PROCESS stage's decision function, as a TYPE rather than as a bool.
//
// The decision underneath it (operations.EffectiveTrust) always computes a
// rich answer — which step decided, and any elaboration on it — so a bare
// bool would throw all of that away and force every caller that has to SAY
// why an item was withheld to re-derive the reason on its own path. One
// Verdict serves both the gate and every report instead: a status report is
// truthful BY CONSTRUCTION rather than because two paths happen to agree
// (docs/trust-model.md).
//
// Exposure carries the whole BundleRead rather than a bare signer string, so
// the three axes the reader established (TrustCtx, Signature, Signer) reach
// the decision as separate facts. A signer string alone cannot express
// INVALID: signing.VerifyPublisher deliberately collapses "unsigned" and
// "signed by a key we do not trust" into "", and a decision keyed on that
// string cannot tell them apart.

// Authorizer decides whether one exposure may be delivered.
//
// It is a PURE FUNCTION of its inputs: no sink, no logging, no side effects. A
// verdict that admits WITH a warning (see ReasonStaleLocalSignature) returns the
// warning in Detail and the CALLER emits it. That is what lets one decision
// serve a delivery path, a listing path and a review report without any of them
// having to re-derive it — and it is why every caller that receives a Verdict is
// obliged to surface a non-empty Detail.
//
// It is content exposure's INSTANTIATION of the admission shape every gate in
// ctxloom takes, not a parallel definition of it: the query is an Exposure and
// the vocabulary is this package's Reason. Companion execution and publish
// destinations instantiate the same shape with their own two types.
type Authorizer = admission.Authorizer[Exposure, Reason]

// AuthorizerFunc adapts a plain function to Authorizer, for a decision that is genuinely
// one expression (and for tests). It carries no state, which is the tell that a
// real authorizer — one that opens countersignature stores — should be a named type
// whose construction says what it reads.
type AuthorizerFunc = admission.AuthorizerFunc[Exposure, Reason]

// Exposure is one item's bytes, about to be delivered, with everything the
// decision keys on.
type Exposure struct {
	// Read is the bundle-level facts the READER established: the trust context
	// (did these bytes cross an intermediary), what the detached signature over
	// the document turned out to be, and what this machine's trust root says
	// about the key behind it. Its axes are unexported and settable only by a
	// reader, so an Exposure built from a struct literal claims NOTHING — it is
	// unclaimed, and a Authorizer withholds it.
	Read BundleRead

	// Ref is the item's identity — the ref the countersignature stores key on.
	Ref trust.Ref

	// Bytes are the EXACT bytes about to be exposed (pre-mustache), NEVER a
	// hash. A hash can only be compared against a recorded hash, and a recorded
	// hash is a file anything can write; bytes can be VERIFIED against a
	// signature (spec §9.3, trap #2). The hash still exists as an index; it just
	// stopped being the authority.
	Bytes []byte

	// Form is the LAYOUT form Bytes are in ("raw" | "distilled"). It is the
	// layout axis ONLY: the composite attestation form a countersignature binds
	// also names the item's ROLE, and that is derived below the decision from
	// Ref.Kind — never named here. A call site therefore cannot assert its own
	// role, which is what stops a text item's approval from satisfying an
	// executable's gate over identical bytes.
	Form ContentForm
}

// Ref spells this exposure's item ref back as the string it was parsed from —
// what an advisory names it by, and what a user types into `ctxloom trust`.
func (e Exposure) RefString() string { return e.Ref.ItemRef() }

// Verdict is what a Authorizer decided, and the ONLY vocabulary anything downstream
// renders. A caller that prints "withheld" without printing Reason is printing
// less than the decision knew.
//
// Its three fields are the shared admission shape's: Allow is whether these
// exact bytes may be delivered, Reason names WHICH rule decided (meaningful for
// admits as well as withholds — "allowed because it is builtin" and "allowed
// because a human countersigned it" are different facts a review surface needs),
// and Detail is the display-only elaboration, never an input to any decision.
//
// On an ADMITTED-WITH-WARNING verdict (ReasonStaleLocalSignature) Detail is the
// warning itself, and every caller must surface it: a Detail nobody prints means
// an author never learns their signature went stale, which is the exact failure
// that row exists to catch.
type Verdict = admission.Decision[Reason]

// Warns reports whether a verdict admits the content AND has something the
// author has to be told. It is the one predicate a caller needs to decide
// whether to emit Detail, so no call site has to know which Reasons are the
// warning ones.
//
// A free function rather than a method because Verdict is an alias for a
// generic instantiation from another package, and Go permits methods only on
// locally-defined types.
func Warns(v Verdict) bool {
	return v.Allow && v.Reason == ReasonStaleLocalSignature && v.Detail != ""
}

// Reason names which rule decided an exposure. It is the single vocabulary the
// gate and every report share: the bundle-level publisher state (PublisherOf)
// and operations.EffectiveTrustResult.Source both project onto it, so a review
// listing and a delivery decision never hold two separate opinions about the
// same bytes.
//
// Source keeps one job Reason does not do: it is the persisted/stamped
// "trust_source" projection, and it names the STEP of the cascade rather than
// what a user is told. Reason is finer where the read has facts the cascade
// never saw — a tampered remote signature and an untrusted signer's key both
// resolve to Source "pending", and they are different sentences with different
// fixes.
type Reason int

// The reasons. Zero is UNSET and no Authorizer emits it: an unpopulated Verdict must
// not read as a decision, and a Verdict{} is a withhold with no reason at all.
const (
	ReasonUnset Reason = iota

	// --- admitted ---

	// ReasonLocal: authored in this project. Trusted by construction.
	ReasonLocal
	// ReasonBuiltin: compiled into this binary. Authenticated BY the binary —
	// trusting ctxloom trusts what it ships.
	ReasonBuiltin
	// ReasonCompanion: a loadout an installed companion binary advertised about
	// itself. Local-equivalent because the binary already ran as the user to
	// produce it; the control point is EXEC consent, not content review.
	ReasonCompanion
	// ReasonTrustedSigner: a key this machine trusts for the publish namespace
	// signed exactly these bytes.
	ReasonTrustedSigner
	// ReasonApproved: a human countersigned exactly these bytes, at this ref, in
	// this form.
	ReasonApproved
	// ReasonStaleLocalSignature: LOCAL content whose sibling signature no longer
	// covers its bytes — the decision table's `local | invalid | *` row. It
	// ADMITS (local content never needed a signature) and WARNS, because the
	// author has silently lost the ability to publish it as signed content and
	// will otherwise not find out until `bundle push` refuses. Detail carries
	// the sentence to print.
	ReasonStaleLocalSignature

	// --- withheld ---

	// ReasonRejected: a human declined this ref, or exactly these bytes.
	// Supreme: it beats every exemption, including builtin and companion.
	ReasonRejected
	// ReasonRetracted: the PUBLISHER withdrew this bundle. Detail carries their
	// stated reason (display-only, untrusted).
	ReasonRetracted
	// ReasonTampered: REMOTE content carrying a signature that does not cover
	// its bytes. Withheld, and deliberately NOT degraded to unsigned/pending:
	// degrading it is the spec §10.2 downgrade — corrupt a `.sig` and signed
	// content becomes merely reviewable, which a careless human then approves.
	ReasonTampered
	// ReasonUnsigned: REMOTE content with no signature at all. Pending review.
	ReasonUnsigned
	// ReasonUntrustedSigner: REMOTE content signed by a key this machine does
	// not trust to publish. Also pending — but with a second fix available
	// (`signer trust`) that "unsigned" does not have, which is exactly
	// why the two are not one reason.
	ReasonUntrustedSigner
	// ReasonPending: nothing positively justified exposure — never reviewed, or
	// the bytes changed since a human approved them. Also the terminal
	// fail-closed answer when the decision could not be established at all (an
	// unreadable approvals store, an unreadable lockfile, an evaluation error).
	ReasonPending
	// ReasonUnaddressable: the ref could not be parsed, so the decision function
	// was never able to key on anything. An item we cannot address is one we
	// cannot justify exposing.
	ReasonUnaddressable
	// ReasonUnestablished: the read claimed nothing — its axes are all unset, so
	// it came from no reader. Zero means unset and unset means withhold, or a
	// struct literal would read as "local, unsigned, no signer".
	ReasonUnestablished

	// --- the gate's own state, appended last so no reason above renumbers ---

	// ReasonUngated: nobody gates this surface, and it says so (AdmitAll). An
	// ADMIT that names the absence of a rule rather than claiming one decided,
	// which is what a management or listing path is honestly doing.
	ReasonUngated
	// ReasonUngoverned: the exposure reached the gate with NO authorizer at all.
	// A fault in the CALLER, not a fact about the content — so it is not
	// reviewable and no human action clears it. Withheld, because the
	// alternative is delivering content nothing ever decided about.
	ReasonUngoverned
)

// NeedsReview reports whether a WITHHELD item is one a human can act on by
// reviewing it — the predicate `ctxloom review` enumerates by.
//
// Rejected and retracted are decided: nothing is pending about them. Tampered is
// deliberately NOT reviewable, and that is the point of separating it from
// "unsigned": presenting tampered bytes for review is the spec §10.2 downgrade
// completing itself — an attacker corrupts a `.sig`, the content lands in the
// review queue looking like ordinary unsigned content, and a human approves it.
// The two fail-closed reasons name a fault to fix, not content to read.
func (r Reason) NeedsReview() bool {
	switch r {
	case ReasonUnsigned, ReasonUntrustedSigner, ReasonPending:
		return true
	default:
		return false
	}
}

// PublisherOf reports what a bundle's READ says about who signed it — the
// bundle-level half of the same vocabulary, for a surface that groups items by
// bundle and has to say what the human is deciding about.
//
// It can say INVALID, which a report derived only from a bundle's signer
// stamps cannot: a stamp names "a principal" or "a fingerprint" and has no
// value for a signature that failed to verify, so that state would read as
// indistinguishable from no signature at all — precisely the state a reviewer
// most needs named.
func PublisherOf(read BundleRead) Reason {
	switch {
	case read.Signature() == SignatureInvalid && read.TrustCtx() == TrustCtxRemote:
		return ReasonTampered
	case read.Signature() == SignatureInvalid:
		return ReasonStaleLocalSignature
	case read.Signature() == SignatureValid && read.Signer() == SignerTrusted:
		return ReasonTrustedSigner
	case read.Signature() == SignatureValid:
		return ReasonUntrustedSigner
	case read.Signature() == SignatureNone:
		return ReasonUnsigned
	default:
		return ReasonUnset
	}
}

// String renders the reason as its stable wire spelling — the token that
// appears in `--format json` output. Changing one of these changes a
// machine-readable contract.
func (r Reason) String() string {
	switch r {
	case ReasonLocal:
		return "local"
	case ReasonBuiltin:
		return "builtin"
	case ReasonCompanion:
		return "companion"
	case ReasonTrustedSigner:
		return "trusted-signer"
	case ReasonApproved:
		return "approved"
	case ReasonStaleLocalSignature:
		return "stale-local-signature"
	case ReasonRejected:
		return "rejected"
	case ReasonRetracted:
		return "retracted"
	case ReasonTampered:
		return "tampered"
	case ReasonUnsigned:
		return "unsigned"
	case ReasonUntrustedSigner:
		return "untrusted-signer"
	case ReasonPending:
		return "pending"
	case ReasonUnaddressable:
		return "unaddressable"
	case ReasonUnestablished:
		return "unestablished"
	case ReasonUngated:
		return "ungated"
	case ReasonUngoverned:
		return "ungoverned"
	default:
		return "unset"
	}
}

// MarshalJSON emits the wire spelling rather than the iota, so a JSON consumer
// reads "untrusted-signer" and not 11 — and so inserting a reason above another
// cannot silently change what an existing consumer sees.
func (r Reason) MarshalJSON() ([]byte, error) {
	return []byte(`"` + r.String() + `"`), nil
}

// Explain renders the short, CONTENT-FREE sentence a user is told when an item
// is withheld or admitted with a warning. It is the single place a Reason turns
// into user-facing words, so every advisory — assembly-time, executable-surface,
// review listing — says the same thing for the same Reason. A withhold must
// never be silent or reasonless (docs/trust-model.md).
//
// detail is the Verdict's own Detail; an empty one simply drops the parenthetical.
func (r Reason) Explain(detail string) string {
	switch r {
	case ReasonRejected:
		return "rejected"
	case ReasonRetracted:
		if detail != "" {
			return "retracted by the publisher (" + detail + ")"
		}
		return "retracted by the publisher"
	case ReasonTampered:
		if detail != "" {
			return "its signature does not cover these bytes — withheld as tampered (" + detail + ")"
		}
		return "its signature does not cover these bytes — withheld as tampered"
	case ReasonUntrustedSigner:
		return "signed by a key this machine does not trust to publish — awaiting review — run 'ctxloom review'"
	case ReasonUnaddressable:
		return "its ref could not be parsed, so nothing could decide about it"
	case ReasonUnestablished:
		return "it reached the gate without established provenance"
	case ReasonUngoverned:
		return "it reached delivery with no authorizer, so nothing decided about it — this is a defect in ctxloom, not in the content"
	case ReasonUngated:
		return "allowed: " + r.String()
	case ReasonStaleLocalSignature:
		if detail != "" {
			return detail
		}
		return "its signature no longer covers its bytes — re-sign it"
	case ReasonLocal, ReasonBuiltin, ReasonCompanion, ReasonTrustedSigner, ReasonApproved:
		return "allowed: " + r.String()
	default:
		// ReasonUnsigned, ReasonPending, ReasonUnset, and the fail-closed
		// default for any reason added without a case here — pending review is
		// the safe, actionable answer and never a bare "withheld". The wording
		// is load-bearing: tests/acceptance/steps_j000200_setup.go asserts on the
		// "awaiting review" substring.
		return "awaiting review — run 'ctxloom review'"
	}
}

// ReportVerdict is what a Authorizer deliberately cannot do: emit.
//
// A Authorizer is a pure function, so everything it wants a human to know comes back
// on the Verdict and the CALLER says it. This is that obligation, in one place,
// so no surface has to know which Reasons carry an obligation and none of them
// can quietly skip it. Every call site that receives a Verdict calls this.
//
// It says exactly two things, and neither is the per-surface "N withheld"
// advisory (that stays with whoever tallies it):
//
//   - An ADMIT WITH A WARNING prints its Detail. A Detail nobody prints means an
//     author never learns their signature went stale — the exact failure the
//     admit-with-warning row exists to catch. WarnOnce, because the sentence
//     names the bundle and every item in that bundle produces the same line.
//   - A TAMPERED withhold raises a trust FINDING, not a warning. Remote bytes
//     whose signature does not cover them are the one withhold that is an attack
//     signal rather than a workflow state, and fail-loudly says a session
//     silently missing that content is the failure to prevent. It is fatal-class
//     in strict mode and warn-and-continue under --degraded, exactly as it was
//     when the loader raised it.
func ReportVerdict(ref string, v Verdict) {
	if Warns(v) {
		clidiag.WarnOnce("ctxloom", "%s", v.Detail)
		return
	}
	if !v.Allow && v.Reason == ReasonTampered {
		strictness.FailOnce(strictness.ClassTrust,
			"re-pull the bundle, or investigate the source — its signature does not cover its bytes",
			"withholding %s: %s", ref, v.Reason.Explain(v.Detail))
	}
}

// UnaddressableReporter is the OPTIONAL capability a Authorizer may expose: "record
// this ref, which I could not even address".
//
// It exists because the ref is parsed ABOVE the Authorizer — Exposure carries a
// parsed trust.Ref, which an unparseable string has no value for — and yet the
// withheld TALLY belongs to the authorizer: the executable surfaces (bundle MCP
// servers, hooks, prompt exports) call Decide with no Pipeline to tally them,
// and their advisory reads the authorizer's own record. Without this seam an item
// withheld because nobody could address it would be withheld SILENTLY on exactly
// those surfaces, which is the one thing a withhold may never be.
//
// It is a type assertion rather than a method on Authorizer so a one-expression
// AuthorizerFunc stays one expression.
type UnaddressableReporter interface {
	Unaddressable(ref string, v Verdict)
}

// Decide is the ONE way an item addressed by a ref STRING reaches a Authorizer.
//
// Every exposure choke funnels through it — the delivery pipeline, the builtin
// and companion fragment resolvers, the bundle MCP/hook resolvers, the profile
// executable gate — so all of them parse the ref the same way, fail closed the
// same way, and discharge the caller's obligation to SPEAK the same way (see
// ReportVerdict). A surface that called a Authorizer directly would be one that
// could forget the last of those.
//
// A NIL authorizer withholds, and says so. "I deliberately have no gate" is
// spelled AdmitAll — a value the caller writes — so nil is left meaning the one
// thing it cannot be a policy for: nobody supplied a gate. Admitting on it would
// make a forgotten gate indistinguishable from an intended one at every call
// site, and would resolve the mistake toward exposure.
//
// An UNPARSEABLE ref withholds. An item nothing can address is an item the
// decision function was never able to key on, and exposing it would be exposing
// content no rule ever saw.
func Decide(authorizer Authorizer, read BundleRead, ref string, payload []byte, form ContentForm) Verdict {
	if authorizer == nil {
		v := Verdict{Reason: ReasonUngoverned}
		clidiag.Warn("ctxloom", "withheld %s: %s", ref, v.Reason.Explain(v.Detail))
		return v
	}
	// AdmitAll answers here, above the parse, so an ungated surface behaves
	// exactly as it did when it was spelled nil — including for a ref nothing
	// can address.
	if !Gates(authorizer) {
		return authorizer.Admit(Exposure{Read: read, Bytes: payload, Form: form})
	}
	tRef, _, _, err := trust.ParseItemRef(ref)
	if err != nil {
		v := Verdict{Reason: ReasonUnaddressable, Detail: err.Error()}
		clidiag.Warn("ctxloom", "withheld %s: %s", ref, v.Reason.Explain(v.Detail))
		if r, ok := authorizer.(UnaddressableReporter); ok {
			r.Unaddressable(ref, v)
		}
		return v
	}
	v := authorizer.Admit(Exposure{Read: read, Ref: tRef, Bytes: payload, Form: form})
	ReportVerdict(ref, v)
	return v
}
