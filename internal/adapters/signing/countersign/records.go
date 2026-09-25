package countersign

import (
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// Records implements composite.ReviewRecords over the two physical
// countersignature stores the signature-envelope spec defines (§9.2):
//
//   - user   — ~/.ctxloom/approvals, personal, never committed. The default
//     write target of `ctxloom review`.
//   - project — .ctxloom/approvals, committable. `ctxloom review --project`
//     writes here; a team/CI inherits it via the project allowed_signers.
//
// Reads are the UNION of both — there is NO precedence between the stores; a
// signature is a signature no matter which one holds it. Precedence lives
// entirely in the DECISION FUNCTION (EffectiveTrust step order), which checks
// Rejected before Approved — so a personal rejection in the user store beats
// an inherited approval sitting in the project store, and vice versa,
// automatically, with no special-casing here (spec §9.2 composition table).
type Records struct {
	user    *Store
	project *Store
	root    trust.TrustRoot
	// fault is the store-RESOLUTION error, held as an error rather than
	// encoded into a value. It is set when the USER store's directory could
	// not be resolved at all (homeApprovalsDir failed — an unresolvable
	// $HOME under a systemd unit, `env -i`, a container with no HOME), which
	// is StateUnconfigured before a Store even exists to ask.
	//
	// It exists because the alternative was `userDir = ""`, and a fault
	// written down as a value is a fault whose CAUSE is gone: the resulting
	// inert store does fail closed, but the only thing it can say about
	// itself afterwards is Store.configured's second-hand guess
	// ("no directory configured (unresolvable home directory?)"), question
	// mark included. readable() returns this first so the error a human can
	// act on is the one they are shown.
	fault error
}

// bothStores is a small ordered-iteration helper; the order is irrelevant to
// the OUTCOME (union has no precedence) but keeping it fixed makes tests
// deterministic when they assert which store's candidate matched.
func (c Records) bothStores() []*Store {
	return []*Store{c.user, c.project}
}

// INVARIANT, and it is about the PAIR rather than either store.
//
// The two stores are not two copies of one thing. The USER store
// (~/.ctxloom/approvals) is personal, machine-global, never committed. The
// PROJECT store (<repo>/.ctxloom/approvals) is committable and is how a team
// or a CI run INHERITS a decision. A container or CI runner therefore has NO
// user store BY DESIGN and draws its trust from the project store alone.
//
// So an ABSENT user store beside a readable project store is a SUPPORTED
// configuration and must keep deciding, not fail the run — and the same holds
// with the two positions swapped, for a project that has never committed an
// approval. Absence is tolerated per-store (Store.Readable), and
// that tolerance is the ONLY reason containerized and CI runs work.
//
// What is NOT tolerated, in either position, is a store that EXISTS and cannot
// be read: that one might be HIDING a rejection, and no amount of health in
// the other store makes it safe to guess. Do not "simplify" this into "either
// store readable is enough" — that reads as an equivalent relaxation and is
// not one. TestCountersignRecords_AbsentUserStore_ProjectStoreStillDecides and
// its UnreadableProjectStore twin hold both halves.
//
// The gap this leaves is named where it lives, in Store.Readable:
// absence cannot be told from "the store failed to mount" without PROVISIONING
// the directory, which is an on-disk-layout decision.
//
// readable probes both physical stores backing this records value,
// distinguishing "neither has been written to yet" (nil — the normal
// fresh-project/fresh-user shape) from "one of them exists but cannot be
// read" (a non-nil error). See Store.Readable's doc for why
// this distinction matters: an unreadable store might be hiding a
// REJECTION, and step 1 of EffectiveTrust is supposed to be supreme. Used
// only by EffectiveTrust's records-construction preamble — Rejected/Approved
// themselves stay pure and never consult this.
// Fault implements composite.Faulted: the reason this pair of stores cannot
// be read, or nil. The gate withholds EVERYTHING on a fault ("could not
// evaluate" never means "allow"), and this raises the trust finding once —
// fatal-class in strict mode, warn-and-continue under --degraded.
func (c Records) Fault() error {
	err := c.readable()
	if err != nil {
		strictness.FailOnce(strictness.ClassTrust, "fix or remove the corrupted approvals store, then re-review (ctxloom review)",
			"approvals store unreadable, denying all items: %v", err)
	}
	return err
}

func (c Records) readable() error {
	if c.fault != nil {
		return fmt.Errorf("user approvals store: %w", c.fault)
	}
	if err := c.user.Readable(); err != nil {
		return fmt.Errorf("user approvals store: %w", err)
	}
	if err := c.project.Readable(); err != nil {
		return fmt.Errorf("project approvals store: %w", err)
	}
	return nil
}

// Rejected reports a rejection covering ref OR exactly these bytes, from
// EITHER store. It must be evaluated for every item, including unsigned ones
// — a rejection is of bytes, not of provenance (mirrors the interface's own
// contract).
//
// The ref-level (sticky) component is unambiguous: it is looked up by the
// exact ref, in either store. The CONTENT component is deliberately
// ref-omitted at signing time (spec §5.3) — but the ReviewRecords interface
// gives Rejected only (ref, payload), no form, so a content-reject candidate
// is searched across every attestation form THIS KIND can be signed under
// (AttestationFormsFor). This mirrors the deleted hash denylist, which was
// form-agnostic by construction (a bare set of hashes); trying every form here
// is a search over a small closed vocabulary, not a security weakening — each
// candidate still must cryptographically verify.
func (c Records) Rejected(ref trust.Ref, payload []byte) bool {
	now := time.Now()

	// A ref with no ref-level address has no ref-level record to find, but the
	// CONTENT component below is ref-omitted by design and still applies — so
	// only the ref-level lookups are skipped, never the whole check. Answering
	// "rejected" here instead would assert a human decision nobody made.
	refStr, addressable := refLevelAddress(ref)
	if addressable && c.refRejected(refStr, now) {
		return true
	}
	return c.contentRejected(ref.Kind, payload, now)
}

// refRejected reports a ref-level rejection of refStr: a verified one in
// either store, or the degraded, UNSIGNED marker (spec §9.5) — checked ONLY
// against the user store. An unsigned marker is exactly as authoritative as
// the deleted trust.yaml design (anything that can write .ctxloom/ can forge
// one), which is why it is never honored from the PROJECT store: that store
// is committable and shared, and an unsigned record there would be a
// forgery primitive with a friendly name.
func (c Records) refRejected(refStr string, now time.Time) bool {
	for _, st := range c.bothStores() {
		if _, ok := st.VerifiedRefReject(refStr, c.root, now); ok {
			return true
		}
	}
	return c.user.HasUnsignedRefReject(refStr)
}

// contentRejected reports a content rejection of payload under any
// attestation form kind can be countersigned under: a verified one in either
// store, or the user store's unsigned marker. An empty payload is never
// content-rejected.
func (c Records) contentRejected(kind trust.ItemKind, payload []byte, now time.Time) bool {
	if len(payload) == 0 {
		return false
	}
	for _, form := range AttestationFormsFor(kind) {
		for _, st := range c.bothStores() {
			if _, ok := st.VerifiedContentReject(form, payload, c.root, now); ok {
				return true
			}
		}
		if c.user.HasUnsignedContentReject(form, payload) {
			return true
		}
	}
	return false
}

// Approved reports that a human approved exactly these bytes, at this ref, in
// this form — checked against EITHER store. An empty payload or form can
// never match (an approval that pinned nothing would be meaningless), so
// those resolve false without even touching the stores.
//
// form is the LAYOUT form the caller resolved (raw or distilled); the
// ATTESTATION form actually looked up is derived here from it and the item's
// kind. That derivation is the reason an approval of a fragment cannot satisfy
// an mcp/hook/skill gate over byte-identical bytes, and it happens HERE — at the
// one point where both axes are in hand — rather than at each gate call site,
// because a call site that could name its own role could name the wrong one.
//
// A kind with no attestation form resolves false, which withholds the item.
func (c Records) Approved(ref trust.Ref, payload []byte, form bundles.ContentForm) bool {
	if len(payload) == 0 || form == "" {
		return false
	}
	attested, err := AttestationFormFor(ref.Kind, layoutOf(form))
	if err != nil {
		clidiag.Warn("ctxloom", "trust: %q cannot be approved (%v) — treating it as unapproved", ref.Key(), err)
		return false
	}
	// An item with no ref-level address has no approval recorded against it,
	// so the answer is "not approved" — fail closed.
	refStr, addressable := refLevelAddress(ref)
	if !addressable {
		return false
	}
	now := time.Now()

	for _, st := range c.bothStores() {
		if _, ok := st.VerifiedApprove(refStr, attested, payload, c.root, now); ok {
			return true
		}
	}
	// Unsigned degraded path (spec §9.5) — user store only, see Rejected.
	return c.user.HasUnsignedApprove(refStr, attested, payload)
}

// AttestationFormFor is the ONE derivation from (live item kind, LAYOUT form)
// to the ATTESTATION form a countersignature binds. Every read and every write
// of a countersignature goes through it, which is what guarantees the two sides
// agree: no call site chooses a role, so no call site can choose the wrong one.
//
// The published mapping. Note the vocabularies do not line up, and the LIVE
// kind is what governs — the composite says "command" while the kind is
// trust.KindPrompt ("prompt", directory "prompts"), a residue of the
// skill→command rename:
//
//	trust.KindFragment + raw       -> fragment/raw
//	trust.KindFragment + distilled -> fragment/distilled
//	trust.KindPrompt   + raw       -> command/raw
//	trust.KindPrompt   + distilled -> command/distilled
//	trust.KindMCP      + raw       -> exec/mcp
//	trust.KindHook     + raw       -> exec/hook
//	trust.KindSkill    + raw       -> skill
//
// mcp, hook and skill have exactly one materialization, so they accept only the
// BASE layout form; asking for their distilled form is a caller bug, not an
// item state, and is refused rather than silently folded onto the single form.
//
// There is deliberately NO mapping for the pre-composite vocabulary. Records
// written under the superseded contract stale, and staleness is only ever
// REPORTED (see Store.LatestApprove) — re-keying one would have to
// guess the role it was approved in, which is the confusion this whole
// vocabulary exists to prevent.
//
// An unrecognized kind is an ERROR, never a passthrough: a kind the surface-type
// registry knows and this function does not can be neither approved nor exposed,
// so extending the registry adds no security surface by construction.
func AttestationFormFor(kind trust.ItemKind, layout signing.Form) (signing.AttestationForm, error) {
	forms, known := attestationForms[kind]
	if !known {
		return signing.AttestNone, fmt.Errorf("no attestation form for item kind %q: it cannot be countersigned", kind)
	}
	form, ok := forms[layout]
	if !ok {
		return signing.AttestNone, fmt.Errorf("no attestation form for item kind %q in form %q", kind, layout)
	}
	return form, nil
}

// attestationForms is AttestationFormFor's table: each countersignable kind,
// and the attestation form each layout it is signed in maps to. Executable
// kinds and skills are signed raw only.
var attestationForms = map[trust.ItemKind]map[signing.Form]signing.AttestationForm{
	trust.KindFragment: {signing.FormRaw: signing.AttestFragmentRaw, signing.FormDistilled: signing.AttestFragmentDistilled},
	trust.KindPrompt:   {signing.FormRaw: signing.AttestCommandRaw, signing.FormDistilled: signing.AttestCommandDistilled},
	trust.KindMCP:      {signing.FormRaw: signing.AttestExecMCP},
	trust.KindHook:     {signing.FormRaw: signing.AttestExecHook},
	trust.KindSkill:    {signing.FormRaw: signing.AttestSkill},
}

// AttestationFormsFor returns every attestation form kind can be countersigned
// under, derived from AttestationFormFor rather than tabulated a second time —
// one table, so the reject search can never look under a form the approve path
// would never write. Empty for a kind with no attestation form at all.
func AttestationFormsFor(kind trust.ItemKind) []signing.AttestationForm {
	var out []signing.AttestationForm
	for _, layout := range []signing.Form{signing.FormRaw, signing.FormDistilled} {
		if form, err := AttestationFormFor(kind, layout); err == nil {
			out = append(out, form)
		}
	}
	return out
}

// CountersignRef builds the canonical item-ref string a countersignature
// binds to.
//
// It used to be ref.CanonicalURL()+"|"+ref.Key(): trust.Ref.Key() alone
// ("<bundle>#<kind>/<name>") omits the repo, so two different repos
// publishing a same-named bundle would otherwise collide, and CanonicalURL()
// was prepended to disambiguate. That "|" join was itself a framing hazard —
// remote.NormalizeRef strips control characters but never "|" (0x7C), so a
// source literally named "S" with bundle "a|b" and a source "S|a" with
// bundle "b" rendered the SAME string and could countersign for each other
// (see trust.BundleRef.Identity's doc, R5). A canonical BundleRef carries
// source, bundle and item in ONE injective string with every component
// percent-encoded, so no component can spell a delimiter of the string that
// contains it — Identity() replaces the join outright rather than picking a
// safer separator.
//
// ref.AsBundleRef can fail: it is a BRIDGE from the wider trust.Ref shape
// (which the decision function still keys on) onto BundleRef's narrower,
// stricter grammar, and a Ref carrying a repository spelling the new grammar
// refuses (see AsBundleRef's doc) or the zero Ref cannot convert. That failure
// is returned as an ERROR and no address, because a store address is the one
// place a stand-in cannot be tolerated: a countersignature written under an
// invented key claims to cover an item nothing can name, and a lookup under
// the same key would report an approval a human never gave. A caller that
// cannot address an item must say which item and stop handling it, not record
// against a placeholder.
func CountersignRef(ref trust.Ref) (string, error) {
	br, err := ref.AsBundleRef()
	if err != nil {
		return "", fmt.Errorf("cannot address %s#%s/%s: %w", ref.Bundle, ref.Kind.Dir(), ref.Name, err)
	}
	return br.Identity(), nil
}

// refLevelAddress reports ref's countersign-store address, and whether ref HAS
// one at all.
//
// INVARIANT: every trust-bearing item lives in a bundle, so every ref converts.
// A conversion failure is therefore a FAULT and is always named — it is a
// spelling the grammar refuses, which a human can act on. The boolean survives
// because the callers must still fail closed on one (an unaddressable ref has
// no ref-level record, so it is neither approved nor rejected at ref level;
// only a CONTENT-scoped decision, which is ref-omitted by design — spec §5.3 —
// can cover it).
func refLevelAddress(ref trust.Ref) (string, bool) {
	refStr, err := CountersignRef(ref)
	if err != nil {
		clidiag.Warn("ctxloom", "trust: %v — no ref-level decision can cover it", err)
		return "", false
	}
	return refStr, true
}

// NewRecords pairs the two stores with the trust root their countersignatures
// verify against. fault is the store-RESOLUTION error, held as an error rather
// than encoded into a value: it is set when the USER store's directory could
// not be resolved at all (HomeDir failed — an unresolvable $HOME under a
// systemd unit, `env -i`, a container with no HOME), which is
// StateUnconfigured before a Store even exists to ask. A fault written down
// as a value is a fault whose CAUSE is gone; Fault returns this first so the
// error a human can act on is the one they are shown.
func NewRecords(user, project *Store, root trust.TrustRoot, fault error) Records {
	return Records{user: user, project: project, root: root, fault: fault}
}

// User is the personal store (~/.ctxloom/approvals).
func (c Records) User() *Store { return c.user }

// Project is the committable store (.ctxloom/approvals).
func (c Records) Project() *Store { return c.project }

// invalidatedApprovalCount reports whether ref/form has a PRIOR approve
// countersignature recorded in the sidecar index — untrusted display
// metadata, never a trust decision (see Store.LatestApprove).
// Used by the re-distill loud path (spec §10.4): the caller has just
// rewritten this item's bytes, so if any prior approve record exists for
// this exact (kind, ref, form), it necessarily covered the OLD bytes — the
// new bytes did not exist when it was signed — and therefore no longer
// verifies. Presence is proof of invalidation, not merely a proxy for it.
// An index this process cannot read is reported rather than read as "no prior
// approval": the caller uses the answer to WARN a user that re-distilling
// just invalidated something they signed, and staying silent about that is
// the failure mode worth avoiding.
//
// layout is the LAYOUT form, not the attestation form: the index is keyed on the
// bump-independent axis so a superseded record still counts as a prior approval
// (see Store.LatestApprove).
func (c Records) HadPriorApprove(refStr string, layout signing.Form) (bool, error) {
	for _, st := range c.bothStores() {
		_, ok, err := st.LatestApprove(refStr, layout)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// layoutOf maps the bundle layout vocabulary onto the signing layout form by
// comparison, never by conversion: a form neither vocabulary names maps to
// FormNone, which no attestation form is derived for.
func layoutOf(form bundles.ContentForm) signing.Form {
	switch form {
	case bundles.FormRaw:
		return signing.FormRaw
	case bundles.FormDistilled:
		return signing.FormDistilled
	default:
		return signing.FormNone
	}
}
