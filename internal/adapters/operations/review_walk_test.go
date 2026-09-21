package operations

import (
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// scriptedObserver is a frontend that answers each item from a script and
// records what the walk told it. When the script runs out it reports EOF, the
// way a closed stdin does.
type scriptedObserver struct {
	answers []ReviewDecision
	bundles []string
	shown   []string
	// recorded is "verb ref" per decision the walk recorded; failed is every
	// ref whose decision could not be recorded.
	recorded []string
	failed   []string
}

func (o *scriptedObserver) BundleStart(b ReviewBundle) { o.bundles = append(o.bundles, b.Ref) }

func (o *scriptedObserver) Decide(_, _ int, item ReviewItem) (ReviewDecision, error) {
	o.shown = append(o.shown, item.Ref)
	if len(o.answers) == 0 {
		return ReviewSkip, io.EOF
	}
	d := o.answers[0]
	o.answers = o.answers[1:]
	return d, nil
}

func (o *scriptedObserver) Recorded(ref string, d ReviewDecision) {
	verb := "trusted"
	if d == ReviewReject {
		verb = "rejected"
	}
	o.recorded = append(o.recorded, verb+" "+ref)
}

func (o *scriptedObserver) NotRecorded(ref string, _ error) { o.failed = append(o.failed, ref) }
func (o *scriptedObserver) ContentNotCountersigned(string)  {}

// recordingApply captures the decisions the walk drives, optionally failing
// chosen refs.
type recordingApply struct {
	accepted []string
	rejected []string
	failRefs map[string]bool
}

func (r *recordingApply) funcs() reviewApplyFuncs {
	return reviewApplyFuncs{
		accept: func(ref string) error {
			if r.failRefs[ref] {
				return fmt.Errorf("boom")
			}
			r.accepted = append(r.accepted, ref)
			return nil
		},
		reject: func(ref string) error {
			if r.failRefs[ref] {
				return fmt.Errorf("boom")
			}
			r.rejected = append(r.rejected, ref)
			return nil
		},
	}
}

// walkFixture is a two-bundle pending set: bundle one with three items (one an
// update), bundle two with two.
func walkFixture() *PendingReviewResult {
	return &PendingReviewResult{
		Total:   5,
		Updates: 1,
		Bundles: []ReviewBundle{
			{
				Ref:       "https://github.com/acme/repo@bundles/one",
				Remote:    "acme",
				Publisher: bundles.ReasonUnsigned,
				Items: []ReviewItem{
					{Ref: "one#fragments/f1", Kind: "fragments", Name: "f1", Status: ReviewStatusNew, CurrentContent: "f1 body"},
					{Ref: "one#commands/s1", Kind: "commands", Name: "s1", Status: ReviewStatusUpdate, CurrentContent: "s1 v2", PreviousContent: "s1 v1"},
					{Ref: "one#mcp/m1", Kind: "mcp", Name: "m1", Status: ReviewStatusNew, Executable: true, CurrentContent: "command: m1\n"},
				},
			},
			{
				Ref:               "https://github.com/acme/repo@bundles/two",
				Publisher:         bundles.ReasonUntrustedSigner,
				SignerFingerprint: "SHA256:qc0G8V6Bhw4mDeLpUEzGmxJmM8LDG1qFCkTgVoMcYpk",
				Items: []ReviewItem{
					{Ref: "two#fragments/f2", Kind: "fragments", Name: "f2", Status: ReviewStatusNew, CurrentContent: "f2 body"},
					{Ref: "two#fragments/f3", Kind: "fragments", Name: "f3", Status: ReviewStatusNew, CurrentContent: "f3 body"},
				},
			},
		},
	}
}

// TestReviewWalk_TrustRejectSkip drives one of each single-item action and
// checks tallies, applied refs, and what the frontend was told.
func TestReviewWalk_TrustRejectSkip(t *testing.T) {
	rec := &recordingApply{}
	obs := &scriptedObserver{answers: []ReviewDecision{ReviewTrust, ReviewReject, ReviewSkip, ReviewTrust, ReviewSkip}}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Equal(t, []string{"one#fragments/f1", "two#fragments/f2"}, rec.accepted)
	assert.Equal(t, []string{"one#commands/s1"}, rec.rejected)
	assert.Equal(t, ReviewWalkResult{Total: 5, Trusted: 2, Rejected: 1, Skipped: 2}, sum)
	assert.Equal(t, 2, sum.StillPending())

	assert.Equal(t, []string{"https://github.com/acme/repo@bundles/one", "https://github.com/acme/repo@bundles/two"}, obs.bundles,
		"every bundle is announced before its items")
	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1", "two#fragments/f2", "two#fragments/f3"}, obs.shown,
		"every item is shown before it is decided")
	assert.Equal(t, []string{"trusted one#fragments/f1", "rejected one#commands/s1", "trusted two#fragments/f2"}, obs.recorded)
	assert.Empty(t, obs.failed)
}

// TestReviewWalk_TrustBundle: a bulk trust covers the current item and
// everything remaining in the SAME bundle without showing them again, then
// the walk moves to the next bundle and asks item by item.
func TestReviewWalk_TrustBundle(t *testing.T) {
	rec := &recordingApply{}
	obs := &scriptedObserver{answers: []ReviewDecision{ReviewTrustBundle, ReviewReject, ReviewSkip}}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1"}, rec.accepted)
	assert.Equal(t, []string{"two#fragments/f2"}, rec.rejected)
	assert.Equal(t, ReviewWalkResult{Total: 5, Trusted: 3, Rejected: 1, Skipped: 1}, sum)
	assert.Equal(t, []string{"one#fragments/f1", "two#fragments/f2", "two#fragments/f3"}, obs.shown,
		"the rest of a bulk-decided bundle is never shown — the reviewer already answered for it")
}

// TestReviewWalk_RejectBundle is the bulk form's other direction, and the one
// that must be proven: bulk TRUST re-gates itself the moment any of those
// bytes change, while every rejection it writes is sticky. The bulk decision
// must stay inside its bundle: an 'R' that ran on to the next bundle would
// reject content the reviewer never saw, permanently.
func TestReviewWalk_RejectBundle(t *testing.T) {
	rec := &recordingApply{}
	obs := &scriptedObserver{answers: []ReviewDecision{ReviewRejectBundle, ReviewTrust, ReviewSkip}}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1"}, rec.rejected)
	assert.Equal(t, []string{"two#fragments/f2"}, rec.accepted,
		"the next bundle is still decided item by item — a bulk answer covers the bundle it was given in, and no more")
	assert.Equal(t, ReviewWalkResult{Total: 5, Trusted: 1, Rejected: 3, Skipped: 1}, sum)
}

// TestReviewWalk_Quit: quit ends the session immediately; nothing after it is
// shown or mutated, and the remainder stays pending.
func TestReviewWalk_Quit(t *testing.T) {
	rec := &recordingApply{}
	obs := &scriptedObserver{answers: []ReviewDecision{ReviewTrust, ReviewQuit}}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Equal(t, []string{"one#fragments/f1"}, rec.accepted)
	assert.Empty(t, rec.rejected)
	assert.Equal(t, 1, sum.Trusted)
	assert.Equal(t, 4, sum.StillPending())
	assert.Len(t, obs.shown, 2)
}

// TestReviewWalk_EOFQuits: a frontend that cannot obtain an answer (closed
// stdin) quits the walk without mutating — no answer, no action.
func TestReviewWalk_EOFQuits(t *testing.T) {
	rec := &recordingApply{}
	obs := &scriptedObserver{}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Empty(t, rec.accepted)
	assert.Empty(t, rec.rejected)
	assert.Equal(t, ReviewWalkResult{Total: 5}, sum)
	assert.Equal(t, 5, sum.StillPending())
}

// TestReviewWalk_ApplyFailureCountsSkipped: a failing mutation is reported to
// the frontend, counts the item as skipped (still pending), and the walk
// continues.
func TestReviewWalk_ApplyFailureCountsSkipped(t *testing.T) {
	rec := &recordingApply{failRefs: map[string]bool{"one#fragments/f1": true}}
	obs := &scriptedObserver{answers: []ReviewDecision{ReviewTrust, ReviewQuit}}
	sum := reviewWalk(walkFixture(), rec.funcs(), obs)

	assert.Empty(t, rec.accepted)
	assert.Equal(t, 0, sum.Trusted)
	assert.Equal(t, 1, sum.Skipped)
	assert.Equal(t, []string{"one#fragments/f1"}, obs.failed)
	assert.Empty(t, obs.recorded)
}

// TestReviewWalk_NothingPending_TouchesNothing: the exported service over an
// empty pending set announces no bundle and records nothing.
func TestReviewWalk_NothingPending_TouchesNothing(t *testing.T) {
	app := fixtureApp(t, config.NewFixture(config.Fixture{AppPaths: []string{t.TempDir()}}))
	obs := &scriptedObserver{}
	sum, err := ReviewWalk(context.Background(), app, ReviewWalkRequest{Pending: &PendingReviewResult{}}, obs)
	require.NoError(t, err)
	assert.Equal(t, ReviewWalkResult{}, sum)
	assert.Empty(t, obs.bundles)
	assert.Empty(t, obs.shown)
}
