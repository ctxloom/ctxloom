package operations

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/afero"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// EffectiveTrustRequest carries the inputs for the trust decision function.
//
// The caller supplies the item Ref, the exact PAYLOAD BYTES about to be exposed
// (pre-mustache), the Form those bytes are in, and the item's verified Signer.
// Bytes, not a hash: a hash can only ever be compared, and this decision must be
// able to VERIFY — a signature is over bytes, and the store's hash index finds a
// candidate signature but never decides anything (spec §9.3, trap #2).
type EffectiveTrustRequest struct {
	Ref     trust.Ref
	Payload []byte
	// Form names which materialization Payload is — the LAYOUT form
	// (bundles.FormRaw or bundles.FormDistilled, as a string; single-form items
	// — mcp, hook, skill — use FormRaw). An approval only allows when it covers
	// THIS form, and the ROLE it must also cover is derived from Ref.Kind rather
	// than named here (see countersignRecords.Approved): a caller cannot assert
	// its own role, so it cannot assert the wrong one. An unknown/empty form,
	// or a kind with no attestation form at all, matches nothing and resolves
	// pending (fail closed).
	Form string
	// Signer is the VERIFIED publisher identity of the document this item was
	// parsed out of (bundles.Bundle.Signer): the principal of an allowed_signers
	// entry whose key made a valid publish signature over that document's bytes,
	// or "builtin:ctxloom", or "" for unsigned. It is an INPUT to the decision,
	// never a state of the item — a signed item with an untrusted key is not a
	// fourth state, it is pending (spec §8, red line 1).
	//
	// It is NEVER a claim read from content. See bundles.Bundle.signer.
	Signer string

	// Records is the review-record backing store: the countersignature stores
	// (spec §9.2), reached through the ReviewRecords seam. Optional: nil builds
	// the default (the user + project countersignature stores, verified against
	// cfg's full trust root — see newCountersignRecords).
	Records ReviewRecords `json:"-"`
	// Retraction is the LOCAL retraction-record seam (RetractionRecords).
	// Optional: nil builds the default (the active lockfile — see
	// buildLockfileRetraction). Never touches the network itself; retraction
	// status is recorded there by sync, which has the network in hand (see
	// internal/remote/retract.go CheckRetracted and operations.syncItem).
	Retraction RetractionRecords `json:"-"`
	FS         afero.Fs          `json:"-"`
}

// ReviewRecords is the seam between the DECISION FUNCTION (steps 1 and 5, whose
// shape is settled) and the STORE that records human review decisions.
//
// It is backed by the countersignature stores (spec §9.2): a human running
// `ctxloom review` countersigns the exact reviewed bytes with their own SSH key,
// and that signature IS the approval record — a hand-edited file is inert noise,
// not an approval, because it does not verify.
//
// Both methods take the PAYLOAD BYTES rather than a hash, which is the shape a
// signature verification needs and a hash comparison does not — so the
// implementation slots in without reshaping the decision function or any of its
// call sites.
type ReviewRecords interface {
	// Rejected reports a rejection covering this ref OR exactly these bytes.
	// The two components are deliberately different scopes: the ref block is
	// sticky (it survives the content changing under the ref), while the content
	// rejection is ref-agnostic (a renamed or moved identical copy stays
	// rejected). Both must be honored, and it must be evaluated for EVERY item —
	// including unsigned ones and ones whose publisher signature failed to
	// verify. A rejection is of bytes, not of provenance.
	Rejected(ref trust.Ref, payload []byte) bool

	// Approved reports that a human approved exactly these bytes, at this ref,
	// in this form. Any change to the exposed bytes must drop the approval and
	// return the item to pending. form is the LAYOUT form; the role the
	// approval must have been recorded in comes from ref.Kind.
	Approved(ref trust.Ref, payload []byte, form string) bool
}

// RetractionRecords is the seam between the DECISION FUNCTION and the LOCAL
// record of publisher retractions.
//
// Retraction status originates at the REMOTE MANIFEST (internal/remote/
// retract.go's CheckRetracted hits the fetcher) but EffectiveTrust is an
// EXPOSURE-TIME, local-only decision — it must never make a network call of
// its own. So the network probe runs at SYNC time (sync already has the
// network in hand — see operations.syncItem) and its verdict is recorded
// locally (the active lockfile, see buildLockfileRetraction); this seam is
// what EffectiveTrust reads back, exactly mirroring how ReviewRecords lets the
// decision function consult a human's review without re-deriving it.
type RetractionRecords interface {
	// Retracted reports whether ref's bundle is currently recorded as
	// retracted, and the publisher's stated reason (display-only). A ref with
	// no local record (never synced, or synced before retraction existed)
	// reports false — fail-closed for EXPOSURE still holds via the ordinary
	// pending default; a missing retraction record is not itself a security
	// gap because sync re-evaluates it for every installed ref (see
	// operations.syncItem's installed-ref retraction re-check).
	Retracted(ref trust.Ref) (retracted bool, reason string)
}

// readableRecords is the OPTIONAL capability a ReviewRecords implementation
// may expose: "can this backing store actually be read". EffectiveTrust
// checks it via a type assertion (never adds it to the ReviewRecords
// interface itself) so the fail-closed gate applies to any REAL,
// disk-backed records value — whether EffectiveTrust built it fresh (the
// records == nil default) or a caller built one the same way and threaded it
// in non-nil, which is what every production caller actually does — while a
// test fake with no physical store to corrupt (fakeRecords) simply doesn't
// implement it and is assumed readable. countersignRecords.readable (see
// countersign_records.go) is the sole production implementation.
type readableRecords interface {
	readable() error
}

// readableRetraction is the RETRACTION-side twin of readableRecords: the
// OPTIONAL capability a RetractionRecords implementation may expose, meaning
// "could retraction state actually be established". EffectiveTrust checks it
// by type assertion rather than widening the RetractionRecords interface, so
// the closure-driven test fakes (fakeRetraction) stay two lines long and are
// simply assumed readable; lockfileRetraction is the sole production
// implementation.
//
// The method name deliberately differs from readableRecords.readable(), so
// the two capabilities are not structurally identical interfaces that any
// implementation of one silently satisfies for the other. They are different
// stores answering different questions, and a type assertion must never
// confuse them.
type readableRetraction interface {
	retractionReadable() error
	lockfilePath() string
}

// EffectiveTrustResult reports the decision outcome and which step decided it.
type EffectiveTrustResult struct {
	Decision trust.Decision `json:"decision"`
	Source   trust.Source   `json:"source"`
	// Detail is an OPTIONAL human-readable elaboration on Source, display-only
	// (never a decision input). Today only step 2 (retraction) populates it,
	// with the publisher's stated retraction reason (see
	// RetractionRecords.Retracted) — the same untrusted, informational string
	// `ctxloom remote pull`'s "Retracted:" bucket already surfaces at sync
	// time (internal/cli/remote.go). Empty for every other Source.
	Detail string `json:"detail,omitempty"`
}

// Trusted reports whether the decision allowed exposure. It is the boolean the
// list-JSON stamp surfaces as "trusted"; Source (as a plain string) is the
// companion "trust_source".
func (r EffectiveTrustResult) Trusted() bool {
	return r.Decision == trust.Allow
}

// State renders the result in the three-state review vocabulary for listings:
// rejected when a rejection OR a retraction decided it (both withhold
// permanently, pending nothing further), accepted for any allow (a reviewed
// acceptance or a first-party exemption — Source says which), and pending for
// every other deny (awaiting review, or fail-closed).
func (r EffectiveTrustResult) State() trust.State {
	switch {
	case r.Source == trust.SourceRejected || r.Source == trust.SourceRetracted:
		return trust.StateRejected
	case r.Decision == trust.Allow:
		return trust.StateAccepted
	default:
		return trust.StatePending
	}
}

// Reason renders a short, content-free, human-readable explanation of WHY a
// withheld item was withheld — deriving entirely from the already-computed
// Source/Detail (never recomputing or re-deriving the decision). This is the
// single place that turns a Source into user-facing words, so every withheld
// advisory (the assembly-time warnWithheld, ExecutableTrustGate.WarnWithheld)
// says the same thing for the same Source. A withhold must never be silent or
// reasonless (see docs/trust-model.md): every caller printing a withheld ref
// pairs it with this string.
//
// Only the three DENY sources are meaningful here (Reason is only ever
// consulted for a withheld item); the default case covers them defensively.
func (r EffectiveTrustResult) Reason() string {
	switch r.Source {
	case trust.SourceRejected:
		return "rejected"
	case trust.SourceRetracted:
		if r.Detail != "" {
			return fmt.Sprintf("retracted by the publisher (%s)", r.Detail)
		}
		return "retracted by the publisher"
	default:
		// trust.SourcePending, and the fail-closed default for any future
		// deny source that forgets to add a case here — pending review is
		// the safe, actionable default, never a bare "withheld". Wording
		// ("awaiting review") matches warnPendingTally's existing phrasing —
		// tests/acceptance/steps_j1_setup.go's "Alice is told the content is
		// held for her review" step asserts on this exact substring.
		return "awaiting review — run 'ctxloom review'"
	}
}

// EffectiveTrust is the sole owner of the per-item trust decision function.
// First-match-wins, fail-closed:
//
//  1. REJECTED       DENY   a rejection covers this ref, or these exact bytes
//
// 2a. UNREADABLE     DENY   retraction state could not be established (remote refs only)
//  2. RETRACTED      DENY   the publisher withdrew this bundle (locally recorded at sync)
//  3. LOCAL          ALLOW  authored in this project
//  4. BUILTIN        ALLOW  compiled into this binary
//  5. TRUSTED SIGNER ALLOW  a key trusted to PUBLISH signed these bytes
//  6. APPROVED       ALLOW  a human approved exactly these bytes, here, in this form
//  7. otherwise      DENY   pending — withheld until a human reviews it
//
// REJECTION IS SUPREME and it is step 1 for a reason: a user must be able to
// reject content signed by the ctxloom release key itself, and to reject a
// builtin. No signature can un-reject anything, and no verification may
// short-circuit ahead of it — step 1 is evaluated even when the publisher
// signature is absent or failed to verify, because a rejection is of BYTES, not
// of provenance.
//
// RETRACTION is step 2, a PEER of rejection rather than folded into it: a
// rejection is a human's decision about bytes; a retraction is the
// PUBLISHER'S own withdrawal, sourced from their remote manifest (see
// internal/remote/retract.go CheckRetracted) rather than a countersignature.
// It is checked this early so it, too, beats every allow below — including a
// trusted signer's own key: a publisher must be able to retract content it
// signed. The check itself is a pure LOCAL lookup (RetractionRecords) — the
// network probe already ran at sync time, which is the only place with the
// network in hand (operations.syncItem); EXPOSURE-time evaluation here never
// dials out.
//
// Step 2 FAILS CLOSED (2a). Because the lookup is local, it has a way of
// coming back with no answer at all — a lock.yaml that exists and will not
// parse. "I cannot read the retraction record" is not "nothing is retracted",
// and collapsing the two re-exposes content a publisher deliberately withdrew,
// which inverts the one control that exists for "this turned out to be
// harmful". So an unreadable lockfile withholds instead, for exactly the refs
// that lockfile could have spoken about (remote ones — see retractable). An
// ABSENT lockfile is not this case and never was: a project with no pins
// legitimately has nothing retracted.
//
// Step 5 is where SIGNATURES enter, and what they buy is authentication, never
// authorization: it allows because a key trusted for the publish namespace
// signed exactly these bytes. It replaces the deleted trust_bundles source
// bypass, which allowed because of the URL the bytes arrived from and never
// looked at the content at all. Signed still does not mean safe — that is why
// review (steps 1 and 6) is a separate axis and why rejection (and retraction)
// outrank every signature, including ours.
//
// Red line: NO STATE WAS ADDED. Signer is an input, never a state. An item is
// pending, approved, or rejected; a signed item whose key you do not trust is
// not a fourth thing — it is pending. Retraction is likewise not a fourth
// STATE (EffectiveTrustResult.State() renders it as rejected — withheld
// permanently, awaiting nothing) — it is a second DENY reason at the top of
// the cascade.
//
// Nothing here reads or writes lock.yaml directly for the REJECTED step (ADR
// 0033): the lockfile pins dependencies and is not a security surface for
// review state. Retraction is the deliberate, narrow exception — see
// buildLockfileRetraction — because the alternative is a network call at
// exposure time, which is worse. Verification happens at the EXPOSURE choke,
// not at fetch or lock time — a pull of an unsigned, badly signed, rejected,
// or retracted bundle SUCCEEDS, and its content is withheld here.
func EffectiveTrust(cfg *config.Config, req EffectiveTrustRequest) (*EffectiveTrustResult, error) {
	records := req.Records
	if records == nil {
		records = buildCountersignRecords(cfg, req.FS, nil, nil, nil)
	}
	// The unreadable-store fail-closed gate runs HERE, unconditionally — not
	// only when records was built fresh above. Every production caller
	// (contentGate.allow, review.go's PendingReview, bundle_distill.go)
	// builds its ReviewRecords ONCE at construction time and threads it into
	// EffectiveTrustRequest.Records non-nil on EVERY call; a check gated on
	// "records == nil" therefore never runs for any of them and is dead code
	// in production — confirmed empirically: reject a local fragment, corrupt
	// the approvals store, re-materialize, and it silently un-rejects.
	// readableRecords is an OPTIONAL capability, not
	// part of the ReviewRecords contract: a test fake that doesn't implement
	// it (fakeRecords) is assumed readable and skips this gate entirely —
	// only a REAL countersignRecords (built here or injected by a caller
	// that built one the same way) is ever asked.
	if rr, ok := records.(readableRecords); ok {
		if err := rr.readable(); err != nil {
			// An approvals store we cannot read may hold REJECTIONS we would
			// otherwise miss — deny everything rather than silently reopen the
			// gate, ahead of every other step (including the local/builtin
			// exemptions below). Fatal-class in strict mode (a deny-all
			// session is not the session the user set up) — mirrors the
			// deleted ledger's own store-open check (getTrustStore).
			//
			// FailOnce, not Fail: this gate now runs on EVERY item (it moved
			// out of the records==nil preamble, which fired at most once per
			// build, onto the per-item production path). A whole session's
			// worth of items hits the SAME unreadable store, so the finding
			// and its warning line are identical every time — FailOnce
			// collapses them to a single abort-listing entry per checkpoint
			// window instead of one per item (a 13-item materialize would
			// otherwise print the same fatal line 13 times).
			strictness.FailOnce(strictness.ClassTrust, "fix or remove the corrupted approvals store, then re-review (ctxloom review)",
				"approvals store unreadable, denying all items: %v", err)
			return decide(trust.Deny, trust.SourcePending), nil
		}
	}
	retraction := req.Retraction
	if retraction == nil {
		retraction = buildLockfileRetraction(cfg, req.FS)
	}

	// 1. REJECTED. Checked FIRST, ahead of every allow — including the trusted
	//    signer and the builtin. Rejection is supreme.
	if records.Rejected(req.Ref, req.Payload) {
		return decide(trust.Deny, trust.SourceRejected), nil
	}
	// 2a. RETRACTION STATE UNREADABLE — the fail-closed arm of step 2.
	//     A lockfile that exists but cannot be parsed does not
	//     say "nothing is retracted"; it says nothing at all. Treating that
	//     silence as "nothing" is failing OPEN on the one control whose whole
	//     purpose is "this content turned out to be harmful", so a retraction
	//     that once withdrew a bundle would silently un-withdraw it. Assume
	//     instead that whatever was retracted STAYS retracted: withhold.
	//
	//     SCOPED to refs the lockfile could ever have spoken about. Unlike the
	//     approvals store — which holds REJECTIONS, and a local fragment can
	//     be rejected, which is why that gate denies everything — the lockfile
	//     records only REMOTE bundle entries. A local or builtin ref has no
	//     lockfile entry by construction, so an unreadable lockfile conceals
	//     nothing about it; denying it would be withholding on the basis of
	//     state that provably could not exist, and would break a project whose
	//     content is entirely first-party. retractable() is the single
	//     predicate shared with lockfileRetraction.Retracted, so the exemption
	//     and the gate can never drift apart.
	//
	//     BELOW step 1, not above it: rejection is supreme, and a rejected
	//     item should be reported as "rejected" — the truthful, more specific
	//     answer the user acts on — rather than as the generic fail-closed
	//     one. Both deny; only the Source differs. (The approvals gate sits
	//     ABOVE step 1 because an unreadable approvals store is what makes
	//     rejection itself unknowable; here rejection is perfectly readable.)
	//
	//     SourcePending, matching the corrupt-approvals-store deny and
	//     trust.SourcePending's own documented role as "the terminal
	//     fail-closed source (unreadable store or registry...)". Deliberately
	//     NOT SourceRetracted: nothing here establishes that the publisher
	//     actually withdrew anything, and SourceRetracted renders as the
	//     permanent StateRejected — claiming a retraction we cannot read would
	//     be as dishonest as ignoring one.
	if rr, ok := retraction.(readableRetraction); ok && retractable(req.Ref) {
		if err := rr.retractionReadable(); err != nil {
			// FailOnce, not Fail, for the same reason the approvals gate uses
			// it: this runs per ITEM, and a whole session's worth of items
			// hits the same broken file with a byte-identical message.
			strictness.FailOnce(strictness.ClassTrust,
				"delete "+rr.lockfilePath()+" and rebuild it (ctxloom remote lock) — the file is left intact, so its holds and retractions can be read by hand first",
				"cannot establish retraction state: %s is unreadable (%v) — withholding remote content rather than treating a withdrawn bundle as trustworthy",
				rr.lockfilePath(), err)
			return decide(trust.Deny, trust.SourcePending), nil
		}
	}
	// 2. RETRACTED. A peer of step 1: the publisher, not a local reviewer,
	//    withdrew this bundle. Checked just as early so it too beats every
	//    exemption below (local/builtin never actually carry a retraction
	//    record — see buildLockfileRetraction — but the ordering is principled
	//    the same way rejection's is: nothing may short-circuit ahead of it).
	if retracted, reason := retraction.Retracted(req.Ref); retracted {
		// SourceRetracted is the one source carrying a display-only
		// elaboration (the publisher-stated reason).
		return &EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourceRetracted, Detail: reason}, nil
	}
	// 3. LOCAL: authored in this project — first-party, every kind, including
	//    executables. Locality is honest: a seeded or cloned bundle stamps its
	//    canonical remote ref, so a COPY of remote content keys as remote and is
	//    not local-trusted. "You wrote it here, you trust it; a clone of it is
	//    not yours."
	if req.Ref.IsLocal {
		return decide(trust.Allow, trust.SourceLocal), nil
	}
	// 4. BUILTIN: compiled into this binary. Authenticated BY the binary —
	//    trusting ctxloom trusts what it ships — and deliberately NOT signed
	//    (signing bytes embedded in the binary doing the verifying is circular).
	//    It is a distinct step, below rejection, precisely so step 1 can reach it.
	if req.Ref.IsBuiltin {
		return decide(trust.Allow, trust.SourceBuiltin), nil
	}
	// 4b. COMPANION: a loadout an installed companion binary advertised about
	//     itself. LOCAL-EQUIVALENT, and the reasoning is order-of-operations
	//     rather than deference: ctxloom reads a loadout by EXECUTING the
	//     binary, so by the time this content exists that binary has already run
	//     arbitrary code as the user. Gating the CONTENT afterwards buys ~nothing
	//     and costs a review prompt for a tool the user deliberately installed.
	//     The control point that DOES have purchase is exec, and that is where
	//     the human decision lives (config.AdmitCompanions — trust-on-first-use
	//     keyed on absolute path + binary hash, first-party names exempt only
	//     from ctxloom's own install directory).
	//
	//     A distinct step below rejection, exactly like builtin, so step 1 still
	//     reaches it — and it does not escape the unreadable-approvals-store
	//     gate above, which denies every item including this one.
	//
	//     A companion's SIGNATURE does not enter this decision in either
	//     direction. A publisher signature protects bytes from an intermediary,
	//     and a loadout has none: it arrives on the stdout of a binary the user
	//     already consented to run. config.ProbeCompanionLoadouts therefore
	//     REPORTS a signature that fails to verify (a stale or mismatched
	//     signature in the companion's own release — a bug signal, not an
	//     attack signal) and seeds the content unattributed, rather than
	//     withholding it. What catches a swapped companion binary is the
	//     hash-keyed exec consent, not this step.
	if req.Ref.IsCompanion {
		return decide(trust.Allow, trust.SourceCompanion), nil
	}
	// 5. TRUSTED SIGNER: a key this machine trusts for the PUBLISH namespace made
	//    a signature over the exact bytes of the document this item came from,
	//    and that signature verified — at load, before any YAML parse.
	//
	//    A non-empty Signer means exactly that and cannot mean anything else:
	//    it is stamped only by signing.VerifyPublisher's return value, which
	//    resolves the principal from allowed_signers (never from any advisory
	//    field in the content) and only after checking the key is trusted FOR
	//    THIS NAMESPACE. A bundle cannot declare its own signer; see
	//    bundles.Bundle.signer.
	//
	//    trust.BuiltinSigner is excluded explicitly. It is a SYNTHETIC identity,
	//    not a cryptographic one — nothing about a builtin is verified beyond
	//    "it shipped inside this binary", which is what step 3 above says. A
	//    builtin is allowed as a BUILTIN, at its own step, and never as a
	//    "trusted publisher". Without this guard, any future path that stamped
	//    the synthetic token onto a non-builtin bundle would silently launder it
	//    into a verified publisher, so the guard is here rather than in the
	//    caller's discipline.
	if req.Signer != "" && req.Signer != trust.BuiltinSigner {
		return decide(trust.Allow, trust.SourceTrustedSigner), nil
	}
	// 6. APPROVED: a human reviewed exactly these bytes, at this ref, in this
	//    form. Any change to the exposed bytes drops the approval and returns the
	//    item to pending.
	if records.Approved(req.Ref, req.Payload, req.Form) {
		return decide(trust.Allow, trust.SourceAccepted), nil
	}
	// 7. Terminal fail-closed default: pending, withheld until reviewed. This is
	//    where unsigned content lands, where signed-but-untrusted-key content
	//    lands, and where content whose bytes changed lands.
	return decide(trust.Deny, trust.SourcePending), nil
}

func decide(d trust.Decision, s trust.Source) *EffectiveTrustResult {
	return &EffectiveTrustResult{Decision: d, Source: s}
}

// lockfileRetraction is the default RetractionRecords: it reads retraction
// status straight off the active lockfile's bundle entries, which is where
// operations.syncItem records what CheckRetracted found the last time the
// network was consulted (see remote.LockEntry.Retracted). It is built fresh
// per EffectiveTrust call when no Retraction is injected — the same
// lazy-default shape as buildCountersignRecords, and just as safe to build
// repeatedly: a missing lockfile degrades to "nothing retracted" (Load()
// returns an empty Lockfile rather than an error), never a crash and never a
// spurious deny.
//
// unreadable is the fail-closed arm: non-nil when the lockfile
// EXISTS but could not be read or parsed, which means retraction state is
// UNKNOWN rather than empty. See readableRetraction and step 2's gate.
type lockfileRetraction struct {
	lock       *remote.Lockfile
	unreadable error
	// path is the lockfile the failure refers to, carried so the diagnostic
	// can name the exact file the user has to fix. Nothing the user typed
	// mentions lock.yaml, so an unnamed file is an undiagnosable abort.
	path string
}

// retractionReadable implements readableRetraction: nil when retraction state
// was established (including the legitimate "no lockfile at all" case),
// non-nil when it could not be.
func (l *lockfileRetraction) retractionReadable() error {
	if l == nil {
		return nil
	}
	return l.unreadable
}

// lockfilePath reports the file the unreadable error refers to (for the
// diagnostic); empty when there is nothing to name.
func (l *lockfileRetraction) lockfilePath() string {
	if l == nil {
		return ""
	}
	return l.path
}

// buildLockfileRetraction loads cfg's active lockfile and wraps it as a
// RetractionRecords.
//
// A load failure is NOT degraded to an empty lockfile. An ABSENT lockfile is
// not a failure at all — a project with no pinned dependencies legitimately
// has nothing retracted, and remote.LockfileManager.Load already turns
// os.IsNotExist into an empty lockfile with a nil error, so that case never
// reaches the branch below. What DOES reach it is a lockfile that exists and
// cannot be read or parsed, and that means retraction state is UNKNOWN, which
// is not the same statement as "nothing is retracted" — see the gate in
// EffectiveTrust for why the difference is the whole defect.
func buildLockfileRetraction(cfg *config.Config, fs afero.Fs) RetractionRecords {
	if cfg == nil {
		// A nil cfg means there is no project to read a lockfile FROM (every
		// production call site threads a real cfg — see contentGate/TrustStamper
		// construction; nil only ever appears in a decision-function unit test
		// exercising the cascade directly). Building a default here would fall
		// back to getFS(nil)'s real OS filesystem and read whatever lock.yaml
		// happens to sit under the test process's cwd — exactly the un-hermetic
		// disk touch buildCountersignRecords' own nil-cfg tests take pains to
		// avoid (HOME override + injected FS). Degrading to "never retracted"
		// is safe: a real caller always has cfg, so this branch never masks a
		// production retraction.
		return &lockfileRetraction{lock: &remote.Lockfile{Bundles: map[string]remote.LockEntry{}}}
	}
	baseDir := getBaseDir(cfg)
	lm := remote.NewLockfileManager(baseDir, remote.WithLockfileFS(getFS(fs)))
	lockfile, err := lm.Load()
	if err != nil {
		// FAIL CLOSED. The read has no safe answer to give, so it refuses to
		// invent one: the empty lockfile substituted here is a placeholder
		// that the step-2 gate never actually consults, kept only so
		// Retracted() stays nil-safe for any caller that skips the gate.
		return &lockfileRetraction{
			lock:       &remote.Lockfile{Bundles: map[string]remote.LockEntry{}},
			unreadable: err,
			path:       lm.Path(),
		}
	}
	return &lockfileRetraction{lock: lockfile}
}

// lockfileKeyForRef reconstructs a bundle ref's lockfile map key
// ("<url>@bundles/<path>") from a trust.Ref — the exact inverse of what
// parseTrustItemRef derives a trust.Ref's RepoURL/Bundle FROM (it parses that
// same string via remote.ParseReference), and the exact string
// bundles.Bundle.contentSourceRef carries as the gate's "source" for a cloned
// bundle (loader.go's gateContent). All three — the lockfile key, the
// trust.Ref, and the gated content's source ref — are the same identity
// spelled three ways; this is where the spellings meet back up.
func lockfileKeyForRef(ref trust.Ref) string {
	return ref.RepoURL + "@" + remote.ItemTypeBundle.DirName() + "/" + ref.Bundle
}

// retractable reports whether the lockfile could ever record a retraction FOR
// this ref. Retraction is a REMOTE-manifest concept: local and builtin items
// have no remote lockfile entry (no RepoURL) and are never retracted by
// construction.
//
// COMPANION refs are deliberately still IN scope here even though they are
// now local-equivalent at the decision function's step 4b, and the asymmetry
// is intentional. A companion ref carries a non-empty RepoURL (the fixed
// ctxloom:companion token), so it reaches this gate, and nothing ever writes
// a lockfile entry under that token — which means step 2a can only ever make
// a companion item MORE withheld when lock.yaml is unreadable, never less.
// Exempting them would be a relaxation of a fail-closed gate that nobody
// asked for, so the scope stays as it was; it is also, empirically, what
// tests/acceptance/features/trust_surface.feature's unreadable-lockfile
// scenario observes on a machine with real companions installed.
//
// This is the ONE predicate defining that scope, deliberately shared by the
// two places that must agree on it: the Retracted() exemption below, and
// EffectiveTrust's step-2a unreadable gate. If they ever disagreed, either a
// retractable ref would slip past the fail-closed gate, or a first-party ref
// would be withheld over state that could not have existed.
func retractable(ref trust.Ref) bool {
	return !ref.IsLocal && !ref.IsBuiltin && ref.RepoURL != ""
}

// Retracted implements RetractionRecords over the wrapped lockfile snapshot.
// Local and builtin items never have a remote lockfile entry (no RepoURL) and
// are never retracted by construction — see retractable.
func (l *lockfileRetraction) Retracted(ref trust.Ref) (bool, string) {
	if l == nil || l.lock == nil || !retractable(ref) {
		return false, ""
	}
	entry, ok := l.lock.GetEntry(remote.ItemTypeBundle, lockfileKeyForRef(ref))
	if !ok || !entry.Retracted {
		return false, ""
	}
	return true, entry.RetractedReason
}

// --- Mutations (the plumbing under `ctxloom trust` / `ctxloom blacklist`) -----

// resolveCountersignStore picks the physical countersignature store a
// mutation writes to: the committable PROJECT store when project is true,
// else the personal USER store (spec §9.2). Injected stores (test seams) win
// outright; production builds the real on-disk store from cfg.
func resolveCountersignStore(cfg *config.Config, fs afero.Fs, project bool, injectedUser, injectedProject *countersign.Store) (store *countersign.Store, name string, err error) {
	f := getFS(fs)
	if project {
		if injectedProject != nil {
			return injectedProject, "project", nil
		}
		return countersign.NewStore(paths.ApprovalsPath(getBaseDir(cfg)), f), "project", nil
	}
	if injectedUser != nil {
		return injectedUser, "user", nil
	}
	home, herr := paths.HomeApprovalsPath()
	if herr != nil {
		return nil, "", fmt.Errorf("cannot resolve the user approvals store: %w", herr)
	}
	return countersign.NewStore(home, f), "user", nil
}

// resolveSignerOrUnsigned resolves the key a review mutation countersigns
// with. An injected signer always wins (tests, and any caller that already
// resolved one). Otherwise it runs the unified zero-config discovery chain
// (internal/signing/agentkey: explicit key, then `git config
// user.signingkey`, then the sole ssh-agent identity — spec §7A.4).
//
// Failing that: a PROJECT-store write hard-errors — spec §9.5 is explicit that
// `ctxloom review --project` "requires a key and refuses to run without one",
// because an unsigned record in a COMMITTABLE store would be a forgery
// primitive with a friendly name. A USER-store write instead degrades to the
// UNSIGNED path (ok=true, unsigned=true) — exactly as safe as the deleted
// trust.yaml design and never silently promoted to the shared store. This
// applies uniformly whether discovery failed with "no key anywhere"
// (*agentkey.NoKeyError) or "ambiguous, didn't guess" (*agentkey.AmbiguousKeyError).
func resolveSignerOrUnsigned(cfg *config.Config, injected ssh.Signer, project bool) (signer ssh.Signer, unsigned bool, err error) {
	if injected != nil {
		return injected, false, nil
	}
	// sign.key feeds the chain's explicit-key slot here exactly as it does for
	// `ctxloom sign` and `ctxloom review`. Omitting it made the trust/blacklist
	// plumbing disagree with the porcelain that calls it.
	var explicitKey string
	if cfg != nil {
		explicitKey = cfg.SignKey()
	}
	discovered, agentErr := agentkey.NewDiscoverer().Discover(context.Background(), explicitKey)
	if agentErr == nil {
		return discovered.Signer, false, nil
	}
	if project {
		return nil, false, fmt.Errorf(
			"no signing key available (%w) — the project store requires a signed countersignature; "+
				"run 'ssh-add' and try again, or record this decision in the personal store instead", agentErr)
	}
	return nil, true, nil
}

// SetItemTrustRequest accepts the currently-resolved version of an item.
type SetItemTrustRequest struct {
	// Ref is the item reference, "<bundle-ref>#<kind>/<name>" where bundle-ref
	// is a canonical URL ref, a ctxloom:local ref, or a plain local bundle name,
	// kind is fragments|commands|mcp|hooks|skills (legacy "prompts" still
	// accepted as an alias for commands). A
	// trailing "@<commit>" on the bundle ref is accepted for resolution;
	// approval pins by content BYTES (a countersignature), not commit.
	Ref string

	// Project writes to the COMMITTABLE project store (spec §9.2) instead of
	// the personal user store. Requires a resolvable signing key — see
	// resolveSignerOrUnsigned.
	Project bool

	// Signer overrides key resolution (test injection). Nil resolves via
	// ssh-agent, falling back to the unsigned degraded path for the user
	// store (never for Project).
	Signer ssh.Signer `json:"-"`

	// UserStore / ProjectStore override the physical countersignature stores
	// (test injection); production builds them from cfg.
	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`

	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetItemTrustResult reports the recorded approval.
type SetItemTrustResult struct {
	Status  string `json:"status"` // "approved"
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// KeyFingerprint is the countersigning key's SHA256 fingerprint, empty
	// when the approval was recorded UNSIGNED (spec §9.5).
	KeyFingerprint string `json:"key_fingerprint,omitempty"`
	Unsigned       bool   `json:"unsigned,omitempty"`
	// Store names which physical store received the countersignature: "user"
	// (personal, default) or "project" (committable).
	Store string `json:"store"`
}

// SetItemTrust records an item as approved by COUNTERSIGNING its current
// content bytes: a signature over the raw form always, and — when the item
// has a distilled form — a SECOND signature over the distilled form, mirroring
// the deleted hash-pair ledger's "both forms reviewed independently" contract
// (spec §5.2). A later change to either exposed form's bytes stops that
// signature from verifying and returns the item to pending; no separate
// bookkeeping is needed for that property, it falls out of signing bytes.
//
// The countersigning key comes from resolveSignerOrUnsigned: an injected
// Signer, else ssh-agent, else (user store only) the unsigned degraded path.
// Alongside the store write it snapshots the approved bytes (content kinds
// only, best-effort) so a later upstream change can be reviewed as a diff.
func SetItemTrust(cfg *config.Config, req SetItemTrustRequest) (*SetItemTrustResult, error) {
	tRef, loadRef, _, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}
	attestations, _, err := itemAttestations(loader, tRef, loadRef)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %q to approve it: %w", req.Ref, err)
	}

	store, storeName, err := resolveCountersignStore(cfg, req.FS, req.Project, req.UserStore, req.ProjectStore)
	if err != nil {
		return nil, err
	}
	signer, unsigned, err := resolveSignerOrUnsigned(cfg, req.Signer, req.Project)
	if err != nil {
		return nil, err
	}

	refStr := countersignRef(tRef)

	var principal string
	if !unsigned {
		principal = ssh.FingerprintSHA256(signer.PublicKey())
	}
	writeApprove := func(a itemAttestation) error {
		if unsigned {
			if err := store.WriteUnsignedApprove(refStr, a.Attested, a.Payload); err != nil {
				return err
			}
		} else if err := store.WriteApprove(refStr, a.Attested, a.Payload, signer); err != nil {
			return err
		}
		// Best-effort: the sidecar index is untrusted display metadata (spec
		// §9.2) that lets `ctxloom review` label a later pending item UPDATE
		// vs NEW and pick a diff base — never an input to any trust decision.
		// It records the LIVE kind and the LAYOUT form, not the attestation
		// form: those labels must stay readable across a contract bump, which
		// is what makes a superseded approval show up as an update rather than
		// as a first-time item.
		_ = store.AppendIndex(countersign.IndexEntry{
			Ref: refStr, Kind: string(tRef.Kind), Form: string(a.Layout), Assertion: string(signing.AssertionApprove),
			Principal: principal, Unsigned: unsigned, PayloadHash: bundles.HashPayload(a.Payload), ReviewedAt: time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}
	var rawHash, distilledHash string
	for _, a := range attestations {
		if err := writeApprove(a); err != nil {
			return nil, fmt.Errorf("countersign %q (%s): %w", req.Ref, a.Attested, err)
		}
		switch a.Layout {
		case signing.FormRaw:
			rawHash = bundles.HashPayload(a.Payload)
		case signing.FormDistilled:
			distilledHash = bundles.HashPayload(a.Payload)
		}
	}

	snapshotAcceptedItemContent(cfg, loader, tRef, loadRef, req.FS, rawHash, distilledHash)

	res := &SetItemTrustResult{
		Status:   "approved",
		Ref:      tRef.Key(),
		RepoURL:  tRef.CanonicalURL(),
		Unsigned: unsigned,
		Store:    storeName,
	}
	if !unsigned {
		// Same fingerprint already computed into principal above.
		res.KeyFingerprint = principal
	}
	return res, nil
}

// SetBlacklistRequest rejects an item.
type SetBlacklistRequest struct {
	Ref string

	// Project writes to the COMMITTABLE project store instead of the
	// personal user store. See SetItemTrustRequest.Project.
	Project bool

	// Signer overrides key resolution (test injection).
	Signer ssh.Signer `json:"-"`

	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`

	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// SetBlacklistResult reports the recorded rejection.
type SetBlacklistResult struct {
	Status  string `json:"status"` // "rejected"
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// ContentForms are the forms a content-reject countersignature was
	// written for (raw and/or distilled); empty if the item could not be
	// resolved (the ref-level block is still written regardless).
	ContentForms   []string `json:"content_forms,omitempty"`
	KeyFingerprint string   `json:"key_fingerprint,omitempty"`
	Unsigned       bool     `json:"unsigned,omitempty"`
	Store          string   `json:"store"`
}

// SetBlacklist records BOTH companion components of a rejection, each as a
// countersignature (spec §5.3): a REF-scoped, form-agnostic "ref-reject" (the
// sticky block — denies this ref regardless of content/version, surviving
// changes), written even when the item cannot be resolved, and a
// REF-OMITTED "content-reject" per form the item currently has (raw, and
// distilled when present) — so a renamed/moved identical copy stays
// rejected wherever it appears (spec §5.3's asymmetry: approve binds the
// ref, reject's content component deliberately does not).
func SetBlacklist(cfg *config.Config, req SetBlacklistRequest) (*SetBlacklistResult, error) {
	tRef, loadRef, _, err := parseTrustItemRef(req.Ref)
	if err != nil {
		return nil, err
	}
	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	store, storeName, err := resolveCountersignStore(cfg, req.FS, req.Project, req.UserStore, req.ProjectStore)
	if err != nil {
		return nil, err
	}
	signer, unsigned, err := resolveSignerOrUnsigned(cfg, req.Signer, req.Project)
	if err != nil {
		return nil, err
	}

	refStr := countersignRef(tRef)

	// Ref-level (sticky) block — the durable guarantee, written even when the
	// item cannot be resolved (e.g. already deleted).
	if unsigned {
		if err := store.WriteUnsignedRefReject(refStr); err != nil {
			return nil, fmt.Errorf("countersign rejection of %q: %w", req.Ref, err)
		}
	} else {
		if err := store.WriteRefReject(refStr, signer); err != nil {
			return nil, fmt.Errorf("countersign rejection of %q: %w", req.Ref, err)
		}
	}

	// Best-effort content component: rejecting must succeed even when the
	// content is gone. The reported forms stay the LAYOUT names the user's
	// mental model uses ("raw", "distilled"); the record itself binds the
	// derived attestation form.
	var forms []string
	if attestations, _, herr := itemAttestations(loader, tRef, loadRef); herr == nil {
		for _, a := range attestations {
			var werr error
			if unsigned {
				werr = store.WriteUnsignedContentReject(a.Attested, a.Payload)
			} else {
				werr = store.WriteContentReject(a.Attested, a.Payload, signer)
			}
			if werr == nil {
				forms = append(forms, string(a.Layout))
			}
		}
	}

	res := &SetBlacklistResult{
		Status:       "rejected",
		Ref:          tRef.Key(),
		RepoURL:      tRef.CanonicalURL(),
		ContentForms: forms,
		Unsigned:     unsigned,
		Store:        storeName,
	}
	if !unsigned {
		res.KeyFingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	}
	return res, nil
}

// --- Ref parsing + hashing helpers -------------------------------------------

// builtinSourcePrefix marks a builtin-bundle source ref, e.g. "builtin:ltk"
// (the exact "builtin:"+name string extractMCPFromBundle/extractHooksFromBundle/
// ResolveBuiltinBundleFragments construct as their gate ref's bundle component —
// see config_bundles.go). It is never produced by anything reading user- or
// remote-controlled input: only the three builtin resolvers, fed exclusively
// from resources.GetBuiltinBundle (compiled into the binary), construct it.
const builtinSourcePrefix = "builtin:"

// parseTrustItemRef splits an item ref "<bundle-ref>#<kind>/<name>" into the
// trust.Ref (repo, bundle path, kind, name, locality), the bundle ref to load
// content from, and any "@<commit>" provenance carried on the bundle ref.
func parseTrustItemRef(ref string) (tRef trust.Ref, loadRef, version string, err error) {
	// Ingest boundary, and the sharpest one in the codebase: this ref arrives
	// from argv (`ctxloom trust <ref>`), from an MCP argument, or from a gate
	// built over bundle-authored names, and its Bundle/Name components are
	// interpolated verbatim into the countersign preimage via countersignRef.
	// The bare-local fallback below accepts ANY token that carries no scheme
	// marker, so without this the grammar never gets a chance to object.
	ref = remote.NormalizeRef(ref)
	base, sel, found := strings.Cut(ref, "#")
	if !found || base == "" {
		return trust.Ref{}, "", "", fmt.Errorf("trust ref %q missing #<kind>/<name> selector", ref)
	}
	kind, name, err := parseTrustSelector(sel)
	if err != nil {
		return trust.Ref{}, "", "", fmt.Errorf("invalid trust ref %q: %w", ref, err)
	}

	if parsed, perr := remote.ParseReference(base); perr == nil {
		return trust.Ref{
			RepoURL: parsed.URL,
			Bundle:  parsed.Path,
			Kind:    kind,
			Name:    name,
			IsLocal: parsed.IsLocal,
			// IsCompanion rides the SAME parse, from the same reference
			// grammar, so the decision function's companion step can never be
			// reached by a ref that did not parse as ctxloom:companion@<bin>.
			// Copied rather than re-derived from the URL string: one parser,
			// one answer.
			IsCompanion: parsed.IsCompanion,
		}, base, parsed.ContentVersion, nil
	}

	// base failed to parse as a canonical/local ref. A builtin bundle's source
	// ref is recognized explicitly (never falls through to the local guess
	// below) so a builtin item carries its own identity in the trust store —
	// distinct from "local" — and is reachable by the rejection step (trust
	// rework: builtins used to bypass the gate entirely, gate=nil).
	if bundle, ok := strings.CutPrefix(base, builtinSourcePrefix); ok {
		return trust.Ref{Bundle: bundle, Kind: kind, Name: name, IsBuiltin: true}, base, "", nil
	}

	// base is still unrecognized. A genuinely local bundle is referenced by a
	// bare name carrying NO scheme marker at all (e.g. "my-tools", "lang/go") —
	// that is the only case this may still resolve to local. Anything that
	// LOOKS like an attempted canonical/local/builtin ref (a URL scheme, a
	// git@ prefix, or the ctxloom:local@ prefix) but failed to parse must NOT
	// be silently downgraded to "local": that would let an unrecognized or
	// malformed source ref bypass the trust gate entirely (the fail-open bug
	// this fixes — a seeded remote bundle whose canonical ref somehow fails to
	// parse must fail CLOSED, not open). Every caller of parseTrustItemRef
	// already treats an error as fail-closed (the content/exec gates withhold,
	// TrustStamper stamps pending, the CLI mutations refuse the operation), so
	// erroring here is safe in every call site.
	if remote.IsSelfContainedRef(base) {
		return trust.Ref{}, "", "", fmt.Errorf(
			"trust ref %q: %q is not a valid canonical or ctxloom:local reference "+
				"(and not a builtin source) — refusing to treat an unrecognized source as local", ref, base)
	}

	// base is a bare token with no scheme marker → a plain local bundle name.
	return trust.Ref{Bundle: base, Kind: kind, Name: name, IsLocal: true}, base, "", nil
}

// The fail-closed/fail-open boundary above — "does this string carry a marker
// saying it was INTENDED as a scheme-qualified reference?" — is
// remote.IsSelfContainedRef. It used to be a second, local copy named
// looksLikeSourceRef, and the copies had drifted: this one recognised any
// "://" but not ctxloom:companion@, so a malformed companion ref was
// downgraded to a first-party local bundle name and auto-trusted at step 3 —
// trusted MORE than a well-formed one. There is now exactly one list, in the
// package that owns the ref grammar, and it is the union of what the two
// copies recognised.

// parseTrustSelector parses a "<kind>/<name>" selector (the part after "#").
func parseTrustSelector(sel string) (trust.ItemKind, string, error) {
	kindDir, name, found := strings.Cut(sel, "/")
	if !found || name == "" {
		return "", "", fmt.Errorf("selector %q must be <kind>/<name>", sel)
	}
	switch kindDir {
	case "fragments":
		return trust.KindFragment, name, nil
	case "commands", "prompts":
		// "commands" is the current spelling (the CLI list emits #commands/<name>);
		// "prompts" is the legacy alias from the prompt→skill rename before it.
		// Both map to trust.KindPrompt so the stored key (KindPrompt.Dir() ==
		// "prompts"), the assembly-time content gate, and existing acceptances
		// stay valid — the content lives in bundle.Commands, which the hash
		// helpers read under KindPrompt.
		//
		// NOTE: "skills" is deliberately NOT an alias here. Before the
		// skill→command rename, "skills" meant this same command kind; it now
		// frees it for the TRUE Agent Skill kind (trust.KindSkill, below) instead
		// — the CLI/review surface already moved off "#skills/" entirely,
		// so nothing production still relies on the old meaning.
		return trust.KindPrompt, name, nil
	case "mcp":
		return trust.KindMCP, name, nil
	case "hooks":
		// name is the hook's "<event>/<index>" identity (carries an inner slash).
		return trust.KindHook, name, nil
	case "skills":
		return trust.KindSkill, name, nil
	default:
		return "", "", fmt.Errorf("unknown item kind %q (want fragments|commands|mcp|hooks|skills)", kindDir)
	}
}

// computeItemPayloadPair loads the bundle and returns the item's (raw,
// distilled) PAYLOAD BYTES — the exact preimages the decision function and any
// signature both key on. rawPayload always covers the raw authored bytes (or the
// canonical executable surface for mcp/hooks, which have no distilled form);
// distilledPayload covers the distilled rewrite and is nil when no distilled
// form exists (or distillation is suppressed via NoDistill).
//
// Bytes are the primitive and hashes are derived from them, never the other
// way round: there is exactly ONE definition of "the bytes of item X in form
// F" in this codebase — bundles.ContentPayload — and everything that needs
// those bytes, or a hash of them, or a signature over them, comes through
// here. Two definitions is the bug.
//
// It also returns the bundle's VERIFIED publisher identity (empty for unsigned),
// so a caller resolving an item by ref gets the same signer the exposure gate
// would see.
//
// It returns BYTES BY LAYOUT FORM and deliberately not by role: callers that
// record a countersignature must go through itemAttestations, which pairs each
// payload with the attestation form derived from the item's kind. Nothing that
// writes a countersignature may pick a form itself.
func computeItemPayloadPair(loader *bundles.Loader, tRef trust.Ref, loadRef string) (rawPayload, distilledPayload []byte, signer string, err error) {
	bundle, err := loader.Load(loadRef)
	if err != nil {
		return nil, nil, "", err
	}
	signer = bundle.Signer()

	// payloadPair extracts both form payloads from the shared distillable-item
	// preimage builder: preferDistilled=false always yields the raw form;
	// preferDistilled=true yields the distilled form exactly when one exists.
	payloadPair := func(payload func(bool) ([]byte, bundles.ContentForm)) ([]byte, []byte) {
		raw, _ := payload(false)
		if p, form := payload(true); form == bundles.FormDistilled {
			return raw, p
		}
		return raw, nil
	}
	switch tRef.Kind {
	case trust.KindFragment:
		frag, ok := bundle.Fragments[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("fragment %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawPayload, distilledPayload = payloadPair(frag.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindPrompt:
		prompt, ok := bundle.Commands[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("prompt %q not found in bundle %q", tRef.Name, loadRef)
		}
		rawPayload, distilledPayload = payloadPair(prompt.ContentPayload)
		return rawPayload, distilledPayload, signer, nil
	case trust.KindMCP:
		mcp, ok := bundle.MCP[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("mcp server %q not found in bundle %q", tRef.Name, loadRef)
		}
		payload, perr := mcp.ContentPayload()
		if perr != nil {
			return nil, nil, "", fmt.Errorf("mcp server %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	case trust.KindHook:
		// tRef.Name is the hook's "<event>/<index>" identity (see Entries()).
		entry, ok := bundle.Hooks.EntryByID(tRef.Name)
		if !ok {
			return nil, nil, "", fmt.Errorf("hook %q not found in bundle %q", tRef.Name, loadRef)
		}
		payload, perr := entry.Hook.ContentPayload()
		if perr != nil {
			return nil, nil, "", fmt.Errorf("hook %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	case trust.KindSkill:
		skill, ok := bundle.Skills[tRef.Name]
		if !ok {
			return nil, nil, "", fmt.Errorf("skill %q not found in bundle %q", tRef.Name, loadRef)
		}
		// A skill has no distilled form (SKILL.md's description IS the
		// progressive-disclosure mechanism; distilling it would defeat that) —
		// one payload, mirroring KindMCP/KindHook.
		// FSDir, not filepath.Dir(bundle.Path): this is a TRUST decision, and
		// the overloaded Path resolves a companion/seeded bundle to "." — the
		// process working directory — so the bytes hashed into the grant would
		// be whatever happened to sit there.
		skillDir, dirErr := bundle.SkillPreimageDir(skill)
		if dirErr != nil {
			return nil, nil, "", fmt.Errorf("skill %q: %w", tRef.Name, dirErr)
		}
		payload, perr := skill.ContentPayload(loader.FS(), skillDir, tRef.Name)
		if perr != nil {
			return nil, nil, "", fmt.Errorf("skill %q payload: %w", tRef.Name, perr)
		}
		return payload, nil, signer, nil
	default:
		return nil, nil, "", fmt.Errorf("unknown item kind %q", tRef.Kind)
	}
}

// itemAttestation is one countersignable materialization of an item: the exact
// payload bytes, the ATTESTATION form a countersignature over them binds, and
// the LAYOUT form the display-only sidecar index is keyed on.
//
// The two forms travel together because they answer different questions and are
// needed in the same breath — what the signature covers, and what a later review
// looks up to say "you approved something here once". Nothing constructs one of
// these by hand.
type itemAttestation struct {
	Attested signing.AttestationForm
	Layout   signing.Form
	Payload  []byte
}

// itemAttestations resolves every countersignable materialization of an item:
// its payload bytes (computeItemPayloadPair — the single definition of "the
// bytes of item X in form F") each paired with the attestation form derived from
// the item's KIND (attestationFormFor). It is the role-aware entry point both
// write paths use, so an approval or a rejection can never be recorded under a
// kind-blind form.
//
// A kind with no attestation form yields an error rather than an empty list: it
// cannot be countersigned, and a caller told "nothing to record" would report a
// decision it never made.
func itemAttestations(loader *bundles.Loader, tRef trust.Ref, loadRef string) ([]itemAttestation, string, error) {
	rawPayload, distilledPayload, signer, err := computeItemPayloadPair(loader, tRef, loadRef)
	if err != nil {
		return nil, "", err
	}
	out := make([]itemAttestation, 0, 2)
	for _, m := range []struct {
		layout  signing.Form
		payload []byte
	}{
		{signing.FormRaw, rawPayload},
		{signing.FormDistilled, distilledPayload},
	} {
		if len(m.payload) == 0 {
			continue
		}
		attested, ferr := attestationFormFor(tRef.Kind, m.layout)
		if ferr != nil {
			return nil, "", ferr
		}
		out = append(out, itemAttestation{Attested: attested, Layout: m.layout, Payload: m.payload})
	}
	if len(out) == 0 {
		// An item with no bytes in any form has nothing to countersign, and
		// returning an empty list would let a caller report "approved" having
		// written no record at all — exit 0, a success message, zero bytes.
		// The store refuses an empty-payload approve for the same reason; this
		// is that refusal reaching the case where there is no payload to offer
		// it in the first place.
		return nil, "", fmt.Errorf("%s has no content in any form: there is nothing to countersign", tRef.Key())
	}
	return out, signer, nil
}

// computeItemPayload resolves the item's CURRENT effective form — distilled when
// cfg prefers it and a distilled form exists, else raw — returning the exact
// bytes assembly would expose, their form, and the bundle's verified signer.
func computeItemPayload(cfg *config.Config, loader *bundles.Loader, tRef trust.Ref, loadRef string) ([]byte, bundles.ContentForm, string, error) {
	rawPayload, distilledPayload, signer, err := computeItemPayloadPair(loader, tRef, loadRef)
	if err != nil {
		return nil, "", "", err
	}
	if cfgPreferDistilled(cfg) && distilledPayload != nil {
		return distilledPayload, bundles.FormDistilled, signer, nil
	}
	return rawPayload, bundles.FormRaw, signer, nil
}

// --- list-JSON stamping -------------------------------------------------------

// TrustStamper resolves effective per-item trust for a single listing, building
// the review-records backing store, remote registry, and bundle loader ONCE and
// reusing them across every item it stamps. This is the listing cost control:
// the decision function is content-keyed, so a naive stamp would re-read the
// countersignature stores / remotes.yaml and re-materialize each item per call;
// the stamper reads the stores once and lets the shared loader cache each
// bundle after its first materialization.
//
// It is read-only and fault-tolerant by construction: a build failure or any
// per-item parse/resolve/hash failure never surfaces as an error — it stamps a
// fail-closed DENY (never "trusted"), so a listing can never crash and a hash
// failure can never produce a trusted stamp. Not safe for concurrent use.
type TrustStamper struct {
	cfg     *config.Config
	loader  *bundles.Loader
	records ReviewRecords
	fs      afero.Fs
}

// TrustStamperOption injects a pre-built dependency, mirroring the loader
// option style. Tests drive the stamper over an in-memory store/loader;
// production builds them from cfg.
type TrustStamperOption func(*TrustStamper)

// WithStampRecords injects a pre-built review-record store, bypassing the
// default countersignature-store construction. Production
// leaves this unset; tests inject a fixture built over an in-memory fs.
func WithStampRecords(r ReviewRecords) TrustStamperOption {
	return func(ts *TrustStamper) { ts.records = r }
}

// WithStampLoader injects a pre-built bundle loader (it must resolve the same
// refs the listing produced).
func WithStampLoader(l *bundles.Loader) TrustStamperOption {
	return func(ts *TrustStamper) { ts.loader = l }
}

// WithStampFS injects the filesystem used to build the records store when it
// is not supplied directly.
func WithStampFS(fs afero.Fs) TrustStamperOption {
	return func(ts *TrustStamper) { ts.fs = fs }
}

// NewTrustStamper builds a stamper for cfg. It never errors: the
// countersignature stores degrade to "no candidates" rather than failing to
// open (unlike the deleted hash-pair trust.yaml, there is no single file whose
// corruption can deny an entire listing).
func NewTrustStamper(cfg *config.Config, opts ...TrustStamperOption) *TrustStamper {
	ts := &TrustStamper{cfg: cfg}
	for _, o := range opts {
		o(ts)
	}
	if ts.loader == nil && cfg != nil {
		ts.loader = bundleLoader(cfg)
	}
	if ts.records == nil {
		ts.records = newCountersignRecords(cfg, ts.fs)
	}
	return ts
}

// ForRef stamps a fragment/prompt/mcp item addressed by its full list ref
// "<source>#<kind>/<name>". It materializes the item's effective content through
// the shared loader (cached per bundle) to get the exact payload bytes + form +
// verified signer the decision function keys on, honoring ShouldUseDistilled. A
// parse/resolve failure stamps a fail-closed DENY (SourcePending): never
// trusted, never an error (fault tolerance + fail-closed for the trust signal).
func (ts *TrustStamper) ForRef(ref string) EffectiveTrustResult {
	tRef, loadRef, _, err := parseTrustItemRef(ref)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	payload, form, signer, err := computeItemPayload(ts.cfg, ts.loader, tRef, loadRef)
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, payload, string(form), signer)
}

// ForLocalMCP stamps a configured (project-local) MCP server, which carries no
// bundle ref and therefore no publisher signature. It resolves the server's
// executable surface (BundleMCP.ContentPayload — Command+Args+Env+Installation)
// as a local mcp item, which the decision function ALLOWS via the first-party
// local exemption at step 3 (the user configured it in this project themselves)
// — unless it has been explicitly rejected at step 1, which beats the
// exemption, as does a retraction at step 2. Retraction never actually fires
// for a local item, but by SCOPE rather than by precedence: retractable()
// excludes a ref with no remote lockfile entry. The distinction matters,
// because it is what makes the exemption safe to sit this low.
// Signing is irrelevant here and its absence is not a downgrade: local content
// never needed a key and still does not.
func (ts *TrustStamper) ForLocalMCP(name string, srv bundles.BundleMCP) EffectiveTrustResult {
	payload, err := srv.ContentPayload()
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	ref := trust.Ref{Kind: trust.KindMCP, Name: name, IsLocal: true}
	return ts.resolve(ref, payload, string(bundles.FormRaw), "")
}

// ForHook stamps a bundle hook addressed by its (source, HookEntry) identity,
// mirroring the exec choke. It resolves the hook's executable surface
// (BundleHook.ContentPayload) through the decision function.
//
// The source ref (canonical for a cloned bundle, the local name for a project
// bundle) is parsed so IsLocal/RepoURL are honest, and the bundle's verified
// SIGNER is resolved through the shared (cached) loader — because the stamp must
// agree with the gate. config.extractHooksFromBundle gates this same hook with
// the bundle's signer in hand; a stamp that omitted it would report "pending"
// for a hook the gate actually exposes, which is a lie in a security display.
func (ts *TrustStamper) ForHook(source string, entry bundles.HookEntry) EffectiveTrustResult {
	tRef, loadRef, _, err := parseTrustItemRef(source + "#hooks/" + entry.ID())
	if err != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	payload, perr := entry.Hook.ContentPayload()
	if perr != nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return ts.resolve(tRef, payload, string(bundles.FormRaw), ts.signerFor(loadRef))
}

// signerFor returns the verified publisher identity of the bundle at loadRef,
// or "" when it is unsigned or cannot be loaded. Fail-safe: an unresolvable
// bundle yields no signer, which means MORE review, never more exposure.
func (ts *TrustStamper) signerFor(loadRef string) string {
	if ts.loader == nil || loadRef == "" {
		return ""
	}
	bundle, err := ts.loader.Load(loadRef)
	if err != nil {
		return ""
	}
	return bundle.Signer()
}

// resolve runs the decision function with the stamper's shared records store,
// so no item re-reads the countersignature stores. It does NOT make the call
// I/O-free: Retraction is left unset, so EffectiveTrust reads and parses the
// active lockfile once per stamped item (measured in
// trust_perkitem_io_test.go). Sharing that too would fix the sample point of
// retraction state for a whole listing, which is a trust decision, not a
// caching one.
func (ts *TrustStamper) resolve(ref trust.Ref, payload []byte, form, signer string) EffectiveTrustResult {
	res, err := EffectiveTrust(ts.cfg, EffectiveTrustRequest{
		Ref:     ref,
		Payload: payload,
		Form:    form,
		Signer:  signer,
		Records: ts.records,
		FS:      ts.fs,
	})
	if err != nil || res == nil {
		return EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending}
	}
	return *res
}
