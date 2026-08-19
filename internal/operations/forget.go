package operations

import (
	"fmt"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The third review mutation: WITHDRAWING a decision.
//
// SetItemTrust and SetBlacklist move an item into the two decided states; this
// moves it back out, to undecided, as if it had never been reviewed. Without
// it the only answer to a decision made in error is the opposite decision, and
// the two are not symmetric: withdrawing an approval by REJECTING the item
// records something far stronger — a block that beats a trusted publisher and
// beats project-local content, and that survives the content changing under
// the ref.
//
// It is the inverse of BOTH writers, and clearing a REJECTION is the case that
// justifies it: an approval at least invalidates itself when the bytes move,
// while a ref-level rejection is sticky by design and nothing else on this
// surface can lift one.

// ForgetItemDecisionRequest withdraws whichever review decision is recorded
// for an item.
type ForgetItemDecisionRequest struct {
	// Ref is the item reference, in the grammar SetItemTrustRequest.Ref
	// documents.
	Ref string

	// Project clears the COMMITTABLE project store instead of the personal
	// user store. It mirrors the write side's flag, and for the same reason:
	// the two stores are independent, so a decision must be withdrawn from
	// the store that holds it.
	//
	// There is deliberately no "both" mode. Clearing a shared team decision is
	// a different act from clearing your own, and a flag that quietly did both
	// would let a personal correction delete a decision the project inherited.
	Project bool

	// UserStore / ProjectStore override the physical countersignature stores
	// (test injection); production builds them from cfg.
	UserStore    *countersign.Store `json:"-"`
	ProjectStore *countersign.Store `json:"-"`

	// Root overrides the trust root the AFTER-state is read under (test
	// injection). It authorizes nothing here — this operation records no
	// assertion — and is used only to answer "is this item still decided by
	// the other store", which must be read exactly as the gate reads it.
	Root signing.TrustRoot `json:"-"`

	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// The two outcomes ForgetItemDecisionResult.Status can report. They are
// different events — a decision withdrawn, and a ref that had none — and a
// caller that renders them alike confirms a mistyped ref as a success.
const (
	ForgetStatusForgotten       = "forgotten"
	ForgetStatusNothingRecorded = "nothing-recorded"
)

// ForgetItemDecisionResult reports what was withdrawn, and what still stands.
type ForgetItemDecisionResult struct {
	// Status is "forgotten" when at least one record went, and
	// "nothing-recorded" when the item was already undecided. The two are
	// different outcomes and a caller that renders them alike teaches a user
	// that a mistyped ref succeeded.
	Status  string `json:"status"`
	Ref     string `json:"ref"`
	RepoURL string `json:"repo_url"`
	// Store names the physical store cleared: "user" or "project".
	Store string `json:"store"`

	// Cleared names the decisions removed — "approval", "rejection", or both
	// (an item can carry an approval of one form and a rejection of another).
	Cleared []string `json:"cleared,omitempty"`
	// ContentForms are the LAYOUT forms whose content-scoped rejection was
	// cleared, mirroring SetBlacklistResult.ContentForms.
	ContentForms []string `json:"content_forms,omitempty"`
	// Records counts the countersignature records removed.
	Records int `json:"records"`

	// ContentResolved reports whether the item's current bytes could be read.
	// They are needed to address the approval and the content-scoped rejection
	// — only the sticky ref block can be cleared without them — so a false
	// here means the clear was PARTIAL and must be reported as such.
	ContentResolved bool `json:"content_resolved"`

	// StillDecided is "rejected" or "approved" when a record in the OTHER
	// store still decides this item, and empty when it is genuinely back to
	// pending. Withdrawing a personal decision cannot lift one the project
	// inherited, and an item that stays withheld after a successful command
	// must never look like one that came back.
	StillDecided string `json:"still_decided,omitempty"`
}

// ClearedAnything reports whether a decision was actually withdrawn.
//
// It exists so a renderer asks the RESULT what happened instead of
// re-deriving it from a count. Two places deciding "was anything cleared" is
// two places that can disagree, and the direction that disagreement runs — a
// success line over a ref nobody ever decided about — is the one that
// confirms a typo as a working command.
func (r ForgetItemDecisionResult) ClearedAnything() bool {
	return r.Status == ForgetStatusForgotten
}

// ForgetItemDecision removes every countersignature record this store holds
// about an item — approval, sticky ref block, and content-scoped rejection —
// returning it to pending.
//
// It writes nothing, so it resolves NO SIGNING KEY and asks for no namespace
// grant. Recording a decision is an assertion others must be able to verify;
// removing your own record is not one, and requiring a key would make the
// UNSIGNED decisions — recorded, by definition, by the users who have no key —
// the only ones that could never be withdrawn.
//
// The failure it is built against is the silent no-op: a command that reports
// an item returned to pending while it stays withheld. So a record that cannot
// be removed is an ERROR rather than a shortfall in a count, a content
// component that could not be addressed is reported (ContentResolved), and a
// decision left standing in the other store is named (StillDecided).
func ForgetItemDecision(cfg *config.Config, req ForgetItemDecisionRequest) (*ForgetItemDecisionResult, error) {
	cat, tRef, key, err := resolveMutationTarget(cfg, req.Loader, req.Ref)
	if err != nil {
		return nil, err
	}
	store, storeName, err := resolveCountersignStore(cfg, req.FS, req.Project, req.UserStore, req.ProjectStore)
	if err != nil {
		return nil, err
	}
	refStr := CountersignRef(tRef)

	res := &ForgetItemDecisionResult{
		Status:  ForgetStatusNothingRecorded,
		Ref:     tRef.Key(),
		RepoURL: tRef.CanonicalURL(),
		Store:   storeName,
	}

	// The sticky ref block first, and unconditionally: it is the component
	// that outlives the content it was recorded about, so it is the one that
	// must be clearable when the item itself is gone.
	rejectRemoved, err := store.ForgetRefReject(refStr)
	if err != nil {
		return nil, fmt.Errorf("clear the rejection of %q: %w", req.Ref, err)
	}

	// Everything else is addressed BY BYTES, so it needs the item's current
	// content. An unresolvable item is not a failure — see above — but it is
	// a partial result and says so.
	approveRemoved := 0
	attestations, _, aerr := itemAttestations(cat, tRef, key)
	res.ContentResolved = aerr == nil
	if res.ContentResolved {
		for _, a := range attestations {
			n, ferr := store.ForgetApprove(refStr, a.Attested, a.Payload)
			if ferr != nil {
				return nil, fmt.Errorf("clear the approval of %q (%s): %w", req.Ref, a.Attested, ferr)
			}
			approveRemoved += n

			// Every attestation form this KIND can be signed under is swept
			// against these bytes, not merely the form the item currently
			// presents them in — because that is exactly the set
			// countersignRecords.Rejected searches. Clearing a narrower set
			// would leave a rejection the gate can still find.
			cleared := 0
			for _, form := range attestationFormsFor(tRef.Kind) {
				n, ferr := store.ForgetContentReject(form, a.Payload)
				if ferr != nil {
					return nil, fmt.Errorf("clear the content rejection of %q (%s): %w", req.Ref, form, ferr)
				}
				cleared += n
			}
			if cleared > 0 {
				res.ContentForms = append(res.ContentForms, string(a.Layout))
			}
			rejectRemoved += cleared
		}
	}

	// The sidecar index is display-only (spec §9.2), so losing it costs a diff
	// base and nothing else — but leaving it behind would show a human "you
	// approved something here once" about a record that no longer exists, and
	// relabel a cleared item as an UPDATE. Best-effort, exactly as the append
	// side is.
	if _, ierr := store.ForgetIndex(refStr); ierr != nil {
		clidiag.Warn("ctxloom", "cleared the decision for %q but could not update the review index: %v", req.Ref, ierr)
	}

	if approveRemoved > 0 {
		res.Cleared = append(res.Cleared, "approval")
	}
	if rejectRemoved > 0 {
		res.Cleared = append(res.Cleared, "rejection")
	}
	res.Records = approveRemoved + rejectRemoved
	if res.Records > 0 {
		res.Status = ForgetStatusForgotten
	}
	res.StillDecided = remainingDecision(cfg, req, tRef, attestations)
	return res, nil
}

// remainingDecision reports a decision that survives this clear, reading BOTH
// stores through the same records adapter the exposure gate uses — never by
// re-deriving what "still decided" means.
//
// This is the honesty check on a one-store operation. `forget` clears the
// personal store or the project store, never both, so a personal withdrawal
// leaves an inherited project rejection standing and the item exactly as
// withheld as it was. Rejection is asked first because it outranks everything
// (EffectiveTrust step 1), which is the same order the gate resolves in.
//
// An item whose content could not be resolved can still be asked about its
// ref-level rejection, which needs no bytes; its approval cannot be, and is
// not guessed at.
func remainingDecision(cfg *config.Config, req ForgetItemDecisionRequest, tRef trust.Ref, attestations []itemAttestation) string {
	records := buildCountersignRecords(cfg, req.FS, req.UserStore, req.ProjectStore, req.Root)
	if records.Rejected(tRef, nil) {
		return "rejected"
	}
	for _, a := range attestations {
		if records.Rejected(tRef, a.Payload) {
			return "rejected"
		}
	}
	for _, a := range attestations {
		if records.Approved(tRef, a.Payload, string(a.Layout)) {
			return "approved"
		}
	}
	return ""
}
