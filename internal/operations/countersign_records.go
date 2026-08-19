package operations

import (
	"fmt"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/allowedsigners"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// countersignRecords implements ReviewRecords over the two physical
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
type countersignRecords struct {
	user    *countersign.Store
	project *countersign.Store
	root    signing.TrustRoot
}

// bothStores is a small ordered-iteration helper; the order is irrelevant to
// the OUTCOME (union has no precedence) but keeping it fixed makes tests
// deterministic when they assert which store's candidate matched.
func (c countersignRecords) bothStores() []*countersign.Store {
	return []*countersign.Store{c.user, c.project}
}

// readable probes both physical stores backing this records value,
// distinguishing "neither has been written to yet" (nil — the normal
// fresh-project/fresh-user shape) from "one of them exists but cannot be
// read" (a non-nil error). See countersign.Store.Readable's doc for why
// this distinction matters: an unreadable store might be hiding a
// REJECTION, and step 1 of EffectiveTrust is supposed to be supreme. Used
// only by EffectiveTrust's records-construction preamble — Rejected/Approved
// themselves stay pure and never consult this.
func (c countersignRecords) readable() error {
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
// (attestationFormsFor). This mirrors the deleted hash denylist, which was
// form-agnostic by construction (a bare set of hashes); trying every form here
// is a search over a small closed vocabulary, not a security weakening — each
// candidate still must cryptographically verify.
func (c countersignRecords) Rejected(ref trust.Ref, payload []byte) bool {
	now := time.Now()

	// A ref with no ref-level address has no ref-level record to find, but the
	// CONTENT component below is ref-omitted by design and still applies — so
	// only the ref-level lookups are skipped, never the whole check. Answering
	// "rejected" here instead would assert a human decision nobody made.
	refStr, addressable := refLevelAddress(ref)
	for _, st := range c.bothStores() {
		if !addressable {
			break
		}
		if _, ok := st.VerifiedRefReject(refStr, c.root, now); ok {
			return true
		}
	}
	// The degraded, UNSIGNED path (spec §9.5) — checked ONLY against the user
	// store. An unsigned marker is exactly as authoritative as the deleted
	// trust.yaml design (anything that can write .ctxloom/ can forge one),
	// which is why it is never honored from the PROJECT store: that store is
	// committable and shared, and an unsigned record there would be a
	// forgery primitive with a friendly name.
	if addressable && c.user.HasUnsignedRefReject(refStr) {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	for _, form := range attestationFormsFor(ref.Kind) {
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
func (c countersignRecords) Approved(ref trust.Ref, payload []byte, form string) bool {
	if len(payload) == 0 || form == "" {
		return false
	}
	attested, err := attestationFormFor(ref.Kind, signing.Form(form))
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

// attestationFormFor is the ONE derivation from (live item kind, LAYOUT form)
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
// REPORTED (see countersign.Store.LatestApprove) — re-keying one would have to
// guess the role it was approved in, which is the confusion this whole
// vocabulary exists to prevent.
//
// An unrecognized kind is an ERROR, never a passthrough: a kind the surface-type
// registry knows and this function does not can be neither approved nor exposed,
// so extending the registry adds no security surface by construction.
func attestationFormFor(kind trust.ItemKind, layout signing.Form) (signing.AttestationForm, error) {
	switch kind {
	case trust.KindFragment:
		switch layout {
		case signing.FormRaw:
			return signing.AttestFragmentRaw, nil
		case signing.FormDistilled:
			return signing.AttestFragmentDistilled, nil
		}
	case trust.KindPrompt:
		switch layout {
		case signing.FormRaw:
			return signing.AttestCommandRaw, nil
		case signing.FormDistilled:
			return signing.AttestCommandDistilled, nil
		}
	case trust.KindMCP:
		if layout == signing.FormRaw {
			return signing.AttestExecMCP, nil
		}
	case trust.KindHook:
		if layout == signing.FormRaw {
			return signing.AttestExecHook, nil
		}
	case trust.KindSkill:
		if layout == signing.FormRaw {
			return signing.AttestSkill, nil
		}
	default:
		return signing.AttestNone, fmt.Errorf("no attestation form for item kind %q: it cannot be countersigned", kind)
	}
	return signing.AttestNone, fmt.Errorf("no attestation form for item kind %q in form %q", kind, layout)
}

// attestationFormsFor returns every attestation form kind can be countersigned
// under, derived from attestationFormFor rather than tabulated a second time —
// one table, so the reject search can never look under a form the approve path
// would never write. Empty for a kind with no attestation form at all.
func attestationFormsFor(kind trust.ItemKind) []signing.AttestationForm {
	var out []signing.AttestationForm
	for _, layout := range []signing.Form{signing.FormRaw, signing.FormDistilled} {
		if form, err := attestationFormFor(kind, layout); err == nil {
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
// A ref carrying NO BUNDLE names an item that lives in the project's own
// configuration rather than in any bundle — a `config.yaml` MCP server is the
// shape (see TrustStamper.ForLocalMCP). There is no bundle to address it
// under, so it has no ref-level address, and only a CONTENT-scoped decision —
// which is ref-omitted by design (spec §5.3) — can cover it. That is a known
// shape, not a fault, so it is reported by the boolean rather than by a
// diagnostic.
//
// Any OTHER conversion failure IS a fault, and is named: a ref that carries a
// bundle and still will not convert is a spelling the grammar refuses, which a
// human can act on.
func refLevelAddress(ref trust.Ref) (string, bool) {
	refStr, err := CountersignRef(ref)
	if err != nil {
		if ref.Bundle != "" {
			clidiag.Warn("ctxloom", "trust: %v — no ref-level decision can cover it", err)
		}
		return "", false
	}
	return refStr, true
}

// buildCountersignRecords builds the countersignRecords for cfg: the user and
// project countersignature stores (an injected store wins outright — the test
// seam every review-surface request struct exposes as UserStore/ProjectStore),
// and a trust root — an injected one wins outright (test seam), else the
// config's full trust root (embedded defaults + user allowed_signers +
// project allowed_signers, unioned — config.Config.TrustRoot) — as the
// namespace/role check every candidate signature must clear before it counts
// as an approval or rejection.
//
// Returning the CONCRETE type (not the ReviewRecords interface) is deliberate:
// callers that also need the sidecar index for display (PendingReview's
// UPDATE/diff detection) can reach the underlying countersign.Store directly,
// while EffectiveTrust and TrustStamper only ever use it through the
// ReviewRecords interface — the seam newCountersignRecords exists for.
func buildCountersignRecords(cfg *config.Config, fs afero.Fs, injectedUser, injectedProject *countersign.Store, injectedRoot signing.TrustRoot) countersignRecords {
	f := getFS(fs)
	baseDir := getBaseDir(cfg)

	user := injectedUser
	if user == nil {
		// An unresolvable home directory used to leave userDir == "", which
		// is NOT "an empty user store" — every read then resolved against the
		// process working directory (see countersign.Store.configured), and
		// every rejection recorded in the real user store went unseen.
		// countersign.Store now refuses to operate on "", so the fail-closed
		// gate in EffectiveTrust fires; naming the cause here is what makes
		// that abort explicable rather than mysterious.
		userDir, err := paths.HomeApprovalsPath()
		if err != nil {
			clidiag.Warn("ctxloom", "cannot locate the user approvals store (%v) — every personal approval and rejection is unreadable this session", err)
			userDir = ""
		}
		user = countersign.NewStore(userDir, f)
	}
	project := injectedProject
	if project == nil {
		project = countersign.NewStore(paths.ApprovalsPath(baseDir), f)
	}

	root := injectedRoot
	if root == nil {
		if cfg != nil {
			root = cfg.TrustRoot()
		} else {
			root = allowedsigners.NewStore()
		}
	}

	return countersignRecords{user: user, project: project, root: root}
}

// newCountersignRecords builds the default ReviewRecords for cfg — the S6
// seam: EffectiveTrust and TrustStamper call this exactly where they used to
// build ledgerRecords over the hash-pair trust.Store.
func newCountersignRecords(cfg *config.Config, fs afero.Fs) ReviewRecords {
	return buildCountersignRecords(cfg, fs, nil, nil, nil)
}

// invalidatedApprovalCount reports whether ref/form has a PRIOR approve
// countersignature recorded in the sidecar index — untrusted display
// metadata, never a trust decision (see countersign.Store.LatestApprove).
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
// (see countersign.Store.LatestApprove).
func (c countersignRecords) hadPriorApprove(refStr string, layout signing.Form) (bool, error) {
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
