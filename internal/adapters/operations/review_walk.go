package operations

import (
	"context"

	"golang.org/x/crypto/ssh"
)

// ReviewDecision is the reviewer's answer for one item.
type ReviewDecision int

const (
	ReviewSkip ReviewDecision = iota
	ReviewTrust
	ReviewReject
	ReviewTrustBundle
	ReviewRejectBundle
	ReviewQuit
)

// ReviewWalkObserver is the frontend the walk drives.
type ReviewWalkObserver interface {
	BundleStart(b ReviewBundle)
	Decide(index, count int, item ReviewItem) (ReviewDecision, error)
	Recorded(ref string, decision ReviewDecision)
	NotRecorded(ref string, err error)
	ContentNotCountersigned(ref string)
}

// ReviewWalkRequest is one review session's inputs.
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

// reviewApplyFuncs are the mutation hooks the walk drives.
type reviewApplyFuncs struct {
	accept func(ref string) error
	reject func(ref string) error
}

func reviewWalk(pending *PendingReviewResult, apply reviewApplyFuncs, obs ReviewWalkObserver) ReviewWalkResult {
	return ReviewWalkResult{}
}

// ReviewWalk walks every pending item and records the reviewer's decisions.
func ReviewWalk(ctx context.Context, app *App, req ReviewWalkRequest, obs ReviewWalkObserver) (ReviewWalkResult, error) {
	return ReviewWalkResult{}, nil
}
