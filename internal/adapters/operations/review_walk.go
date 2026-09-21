package operations

import (
	"context"

	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/core/config"
)

// The interactive half of `ctxloom review`: walk everything pending, grouped
// by bundle, and record the human's trust/reject decision by COUNTERSIGNING
// the exact reviewed bytes with their own key — the signature IS the
// approval record. The walk decides nothing about what to show or how: a
// ReviewWalkObserver is the frontend, and it is asked for each decision.

// ReviewDecision is the reviewer's answer for one item. The bulk forms cover
// the REST OF ONE BUNDLE and are reset at the next bundle, so a reviewer can
// never decide, in one answer, about content they were never shown.
type ReviewDecision int

const (
	// ReviewSkip leaves the item pending. It is the zero value: an
	// unrecognised answer lands on the safe side. Viewing must never mutate
	// trust.
	ReviewSkip ReviewDecision = iota
	// ReviewTrust countersigns an approval over the item's current bytes.
	ReviewTrust
	// ReviewReject countersigns a refusal that is sticky.
	ReviewReject
	// ReviewTrustBundle trusts this item and the rest of its bundle.
	ReviewTrustBundle
	// ReviewRejectBundle rejects this item and the rest of its bundle.
	ReviewRejectBundle
	// ReviewQuit ends the session; the remainder stays pending.
	ReviewQuit
)

// ReviewWalkObserver is the frontend the walk drives: it shows and asks, the
// walk applies and tallies.
type ReviewWalkObserver interface {
	// BundleStart announces the bundle whose items follow.
	BundleStart(b ReviewBundle)
	// Decide shows item index (1-based) of count and returns the reviewer's
	// answer. An error is "no answer could be obtained" (EOF, a closed
	// stdin) and quits the walk: no answer, no mutation.
	Decide(index, count int, item ReviewItem) (ReviewDecision, error)
	// Recorded reports a decision written — ReviewTrust or ReviewReject, a
	// bulk answer collapsing to the verb it applied.
	Recorded(ref string, decision ReviewDecision)
	// NotRecorded reports a decision that could not be written; the item
	// counts as skipped and stays pending for a later session.
	NotRecorded(ref string, err error)
	// ContentNotCountersigned reports a rejection whose ref-level record was
	// written but whose content could not be countersigned.
	ContentNotCountersigned(ref string)
}

// ReviewWalkRequest is one review session's inputs: the pending set, the
// store the decisions go to and the key that countersigns them — resolved
// ONCE for the whole session, so a session countersigns consistently with
// one key, to one store.
type ReviewWalkRequest struct {
	Pending *PendingReviewResult
	Project bool
	Signer  ssh.Signer
}

// ReviewWalkResult tallies one review session.
type ReviewWalkResult struct {
	Total    int
	Trusted  int
	Rejected int
	Skipped  int
}

// StillPending is what the session left undecided.
func (r ReviewWalkResult) StillPending() int { return r.Total - r.Trusted - r.Rejected }

// ReviewWalk walks every pending item and records the reviewer's decisions
// through the SAME operations the standalone trust/blacklist commands use, so
// the porcelain and the plumbing write identical countersignatures. The only
// error is a configuration that cannot be read; the walk itself is fault
// tolerant (one unresolvable item never aborts the session).
func ReviewWalk(ctx context.Context, app *App, req ReviewWalkRequest, obs ReviewWalkObserver) (ReviewWalkResult, error) {
	cfg, err := app.Config(ctx)
	if err != nil {
		return ReviewWalkResult{}, err
	}
	return reviewWalk(req.Pending, reviewApplier(cfg, req.Project, req.Signer, obs), obs), nil
}

// reviewApplyFuncs are the mutation hooks the walk drives — a seam so the
// walk is unit-testable without resolving real bundles; ReviewWalk wires the
// plumbing (reviewApplier).
type reviewApplyFuncs struct {
	accept func(ref string) error
	reject func(ref string) error
}

// reviewApplier routes accept/reject through SetItemTrust and SetBlacklist.
func reviewApplier(cfg *config.Config, project bool, signer ssh.Signer, obs ReviewWalkObserver) reviewApplyFuncs {
	return reviewApplyFuncs{
		accept: func(ref string) error {
			_, err := SetItemTrust(cfg, SetItemTrustRequest{Ref: ref, Project: project, Signer: signer})
			return err
		},
		reject: func(ref string) error {
			res, err := SetBlacklist(cfg, SetBlacklistRequest{Ref: ref, Project: project, Signer: signer})
			if err == nil && len(res.ContentForms) == 0 {
				obs.ContentNotCountersigned(ref)
			}
			return err
		},
	}
}

// reviewWalk is the walk: per bundle announce it, per item ask and apply.
// A bulk answer applies to the REST OF ONE BUNDLE and is reset at the next
// bundle, so it never reaches content the reviewer was not shown.
func reviewWalk(pending *PendingReviewResult, apply reviewApplyFuncs, obs ReviewWalkObserver) ReviewWalkResult {
	sum := ReviewWalkResult{Total: pending.Total}
	trust := func(ref string) { applyReviewDecision(obs, apply.accept, ref, ReviewTrust, &sum.Trusted, &sum.Skipped) }
	reject := func(ref string) { applyReviewDecision(obs, apply.reject, ref, ReviewReject, &sum.Rejected, &sum.Skipped) }
	for _, b := range pending.Bundles {
		obs.BundleStart(b)
		var rest func(string)
		for i, item := range b.Items {
			if rest != nil {
				rest(item.Ref)
				continue
			}
			decision, err := obs.Decide(i+1, len(b.Items), item)
			if err != nil {
				return sum // no answer → quit; no answer, no mutation
			}
			switch decision {
			case ReviewTrust:
				trust(item.Ref)
			case ReviewReject:
				reject(item.Ref)
			case ReviewTrustBundle:
				rest = trust
				trust(item.Ref)
			case ReviewRejectBundle:
				rest = reject
				reject(item.Ref)
			case ReviewQuit:
				return sum
			default:
				sum.Skipped++
			}
		}
	}
	return sum
}

// applyReviewDecision runs one mutation, reports the outcome, and tallies it.
// A failure counts the item as skipped — it stays pending for a later session
// rather than sinking this one.
func applyReviewDecision(obs ReviewWalkObserver, apply func(string) error, ref string, decision ReviewDecision, tally, skipped *int) {
	if err := apply(ref); err != nil {
		obs.NotRecorded(ref, err)
		*skipped++
		return
	}
	obs.Recorded(ref, decision)
	*tally++
}
