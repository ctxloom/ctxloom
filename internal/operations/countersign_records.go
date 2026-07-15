package operations

import (
	"fmt"
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
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
	// now is a seam for tests to pin time; nil means time.Now.
	now func() time.Time
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

func (c countersignRecords) timeNow() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
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
// is searched across every closed Form value it could have been signed
// under. This mirrors the deleted hash denylist, which was form-agnostic by
// construction (a bare set of hashes); trying every form here is a search
// over a small closed vocabulary, not a security weakening — each candidate
// still must cryptographically verify.
func (c countersignRecords) Rejected(ref trust.Ref, payload []byte) bool {
	kind := signingKindOf(ref.Kind)
	refStr := countersignRef(ref)
	now := c.timeNow()

	for _, st := range c.bothStores() {
		if _, ok := st.VerifiedRefReject(kind, refStr, c.root, now); ok {
			return true
		}
	}
	// The degraded, UNSIGNED path (spec §9.5) — checked ONLY against the user
	// store. An unsigned marker is exactly as authoritative as the deleted
	// trust.yaml design (anything that can write .ctxloom/ can forge one),
	// which is why it is never honored from the PROJECT store: that store is
	// committable and shared, and an unsigned record there would be a
	// forgery primitive with a friendly name.
	if c.user.HasUnsignedRefReject(kind, refStr) {
		return true
	}
	if len(payload) == 0 {
		return false
	}
	for _, form := range contentRejectForms {
		for _, st := range c.bothStores() {
			if _, ok := st.VerifiedContentReject(kind, form, payload, c.root, now); ok {
				return true
			}
		}
		if c.user.HasUnsignedContentReject(kind, form, payload) {
			return true
		}
	}
	return false
}

// contentRejectForms are the closed Form values a content-reject
// countersignature could have been signed under (spec §3.2's table): raw and
// distilled text forms, and the exec (mcp/hooks) canonical-JSON preimage.
var contentRejectForms = []signing.Form{signing.FormRaw, signing.FormDistilled, signing.FormExec}

// Approved reports that a human approved exactly these bytes, at this ref, in
// this form — checked against EITHER store. An empty payload or form can
// never match (an approval that pinned nothing would be meaningless), so
// those resolve false without even touching the stores.
func (c countersignRecords) Approved(ref trust.Ref, payload []byte, form string) bool {
	if len(payload) == 0 || form == "" {
		return false
	}
	kind := signingKindOf(ref.Kind)
	refStr := countersignRef(ref)
	now := c.timeNow()

	for _, st := range c.bothStores() {
		if _, ok := st.VerifiedApprove(kind, refStr, signing.Form(form), payload, c.root, now); ok {
			return true
		}
	}
	// Unsigned degraded path (spec §9.5) — user store only, see Rejected.
	if c.user.HasUnsignedApprove(kind, refStr, signing.Form(form), payload) {
		return true
	}
	return false
}

// signingKindOf maps trust's item-kind vocabulary onto signing's closed
// ItemKind vocabulary. The two disagree on TWO spellings:
//   - trust.KindPrompt vs signing.KindSkills, because trust.ItemKind predates
//     the "skills" (skill->command) rename and signing.ItemKind was authored
//     after it;
//   - trust.KindSkill (the TRUE Agent Skill kind, Part B2) vs
//     signing.KindAgentSkills, because signing.KindSkills was already taken by
//     the legacy mapping above and must not be reused (it is baked into
//     existing command approval/rejection payloads).
//
// This is the single place that reconciles both.
func signingKindOf(k trust.ItemKind) signing.ItemKind {
	switch k {
	case trust.KindFragment:
		return signing.KindFragments
	case trust.KindPrompt:
		return signing.KindSkills
	case trust.KindMCP:
		return signing.KindMCP
	case trust.KindHook:
		return signing.KindHooks
	case trust.KindSkill:
		return signing.KindAgentSkills
	default:
		return signing.ItemKind(k)
	}
}

// countersignRef builds the canonical item-ref string a countersignature
// binds to. trust.Ref.Key() alone ("<bundle>#<kind>/<name>") omits the repo,
// so two different repos publishing a same-named bundle would otherwise
// collide; CanonicalURL() is prepended to disambiguate. Both components come
// from trust.Ref's OWN canonicalization (CanonicalRepoURL / Key), never from
// however a ref string happened to be typed, so this is stable and collision
// -free by the same construction the rest of the trust package relies on.
func countersignRef(ref trust.Ref) string {
	return ref.CanonicalURL() + "|" + ref.Key()
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
		userDir := ""
		if home, err := paths.HomeApprovalsPath(); err == nil {
			userDir = home
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
func (c countersignRecords) hadPriorApprove(kind signing.ItemKind, refStr string, form signing.Form) bool {
	for _, st := range c.bothStores() {
		if _, ok := st.LatestApprove(kind, refStr, form); ok {
			return true
		}
	}
	return false
}
