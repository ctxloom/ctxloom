package cli

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestParseReviewChoice covers the menu parse: only explicit letters act, the
// accept-all shortcut is the uppercase 'A' only, and everything else —
// including the empty line and garbage — is a skip, so viewing never mutates.
func TestParseReviewChoice(t *testing.T) {
	cases := map[string]reviewDecision{
		"a":      reviewAccept,
		" a ":    reviewAccept,
		"A":      reviewAcceptBundle,
		" A ":    reviewAcceptBundle,
		"r":      reviewReject,
		"R":      reviewReject,
		"q":      reviewQuit,
		"Q":      reviewQuit,
		"s":      reviewSkip,
		"":       reviewSkip,
		"skip":   reviewSkip,
		"accept": reviewSkip, // only single-letter shortcuts act
		"yes":    reviewSkip,
		"junk":   reviewSkip,
	}
	for in, want := range cases {
		assert.Equalf(t, want, parseReviewChoice(in), "parseReviewChoice(%q)", in)
	}
}

// scriptedPrompt returns a prompt func that replays answers in order and then
// reports EOF, mimicking a closed stdin.
func scriptedPrompt(answers ...string) func(string) (string, error) {
	i := 0
	return func(string) (string, error) {
		if i >= len(answers) {
			return "", io.EOF
		}
		a := answers[i]
		i++
		return a, nil
	}
}

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
func walkFixture() *operations.PendingReviewResult {
	return &operations.PendingReviewResult{
		Total:   5,
		Updates: 1,
		Bundles: []operations.ReviewBundle{
			{
				Ref:    "https://github.com/acme/repo@bundles/one",
				Remote: "acme",
				Items: []operations.ReviewItem{
					{Ref: "one#fragments/f1", Kind: "fragments", Name: "f1", Status: operations.ReviewStatusNew, CurrentContent: "f1 body"},
					{Ref: "one#commands/s1", Kind: "commands", Name: "s1", Status: operations.ReviewStatusUpdate, CurrentContent: "s1 v2", PreviousContent: "s1 v1"},
					{Ref: "one#mcp/m1", Kind: "mcp", Name: "m1", Status: operations.ReviewStatusNew, Executable: true, CurrentContent: "command: m1\n"},
				},
			},
			{
				Ref: "https://github.com/acme/repo@bundles/two",
				Items: []operations.ReviewItem{
					{Ref: "two#fragments/f2", Kind: "fragments", Name: "f2", Status: operations.ReviewStatusNew, CurrentContent: "f2 body"},
					{Ref: "two#fragments/f3", Kind: "fragments", Name: "f3", Status: operations.ReviewStatusNew, CurrentContent: "f3 body"},
				},
			},
		},
	}
}

// TestRunReviewWalk_AcceptRejectSkip drives one of each single-item action and
// checks tallies, applied refs, and the update diff rendering.
func TestRunReviewWalk_AcceptRejectSkip(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	// f1 accept, s1 reject, m1 skip, f2 accept, f3 skip.
	sum := runReviewWalk(&out, scriptedPrompt("a", "r", "s", "a", ""), walkFixture(), rec.funcs())

	assert.Equal(t, []string{"one#fragments/f1", "two#fragments/f2"}, rec.accepted)
	assert.Equal(t, []string{"one#commands/s1"}, rec.rejected)
	assert.Equal(t, 2, sum.accepted)
	assert.Equal(t, 1, sum.rejected)
	assert.Equal(t, 2, sum.skipped)
	assert.Equal(t, 2, sum.stillPending())

	text := out.String()
	assert.Contains(t, text, "bundles/one (remote: acme) — 3 pending (1 update(s))", "bundle header must name source + counts")
	assert.Contains(t, text, "f1 body", "NEW items show full content")
	assert.Contains(t, text, "-s1 v1", "UPDATE items show a unified diff against the accepted snapshot")
	assert.Contains(t, text, "+s1 v2")
	assert.Contains(t, text, "command: m1", "executables render what they run")
}

// TestRunReviewWalk_AcceptBundle: 'A' accepts the current item and everything
// remaining in the SAME bundle, then the walk moves to the next bundle.
func TestRunReviewWalk_AcceptBundle(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	// f1: A (accepts f1, s1, m1 without further prompts), f2: r, f3: s.
	sum := runReviewWalk(&out, scriptedPrompt("A", "r", "s"), walkFixture(), rec.funcs())

	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1"}, rec.accepted)
	assert.Equal(t, []string{"two#fragments/f2"}, rec.rejected)
	assert.Equal(t, 3, sum.accepted)
	assert.Equal(t, 1, sum.rejected)
	assert.Equal(t, 1, sum.skipped)
}

// TestRunReviewWalk_Quit: 'q' ends the session immediately; nothing after it
// is prompted or mutated, and the remainder stays pending.
func TestRunReviewWalk_Quit(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	sum := runReviewWalk(&out, scriptedPrompt("a", "q"), walkFixture(), rec.funcs())

	assert.Equal(t, []string{"one#fragments/f1"}, rec.accepted)
	assert.Empty(t, rec.rejected)
	assert.Equal(t, 1, sum.accepted)
	assert.Equal(t, 4, sum.stillPending())
}

// TestRunReviewWalk_EOFQuits: a read error (closed stdin) quits without
// mutating — no answer, no action.
func TestRunReviewWalk_EOFQuits(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	sum := runReviewWalk(&out, scriptedPrompt(), walkFixture(), rec.funcs())

	assert.Empty(t, rec.accepted)
	assert.Empty(t, rec.rejected)
	assert.Zero(t, sum.accepted+sum.rejected+sum.skipped)
	assert.Equal(t, 5, sum.stillPending())
}

// TestRunReviewWalk_ApplyFailureCountsSkipped: a failing mutation warns,
// counts the item as skipped (still pending), and the walk continues.
func TestRunReviewWalk_ApplyFailureCountsSkipped(t *testing.T) {
	rec := &recordingApply{failRefs: map[string]bool{"one#fragments/f1": true}}
	var out bytes.Buffer
	sum := runReviewWalk(&out, scriptedPrompt("a", "q"), walkFixture(), rec.funcs())

	assert.Empty(t, rec.accepted)
	assert.Equal(t, 0, sum.accepted)
	assert.Equal(t, 1, sum.skipped)
}

// TestPrintReviewItem_UpdateWithoutSnapshot falls back to full content with an
// explicit note when an UPDATE has no snapshot (e.g. a migrated v1 grant).
func TestPrintReviewItem_UpdateWithoutSnapshot(t *testing.T) {
	var out bytes.Buffer
	printReviewItem(&out, 1, 1, operations.ReviewItem{
		Ref: "b#fragments/x", Kind: "fragments", Name: "x",
		Status: operations.ReviewStatusUpdate, CurrentContent: "current full body",
	})
	assert.Contains(t, out.String(), "no snapshot of the previously accepted content")
	assert.Contains(t, out.String(), "current full body")
}

// TestRenderReviewList pins the non-TTY table: counts, per-bundle grouping,
// new|update column, and the interactive hint.
func TestRenderReviewList(t *testing.T) {
	var out bytes.Buffer
	renderReviewList(&out, walkFixture())
	text := out.String()
	assert.Contains(t, text, "5 item(s) pending review (1 update(s))")
	assert.Contains(t, text, "https://github.com/acme/repo@bundles/one (remote: acme)")
	assert.Contains(t, text, "new      fragments/f1")
	assert.Contains(t, text, "update   commands/s1")
	assert.Contains(t, text, "Run 'ctxloom review' in a terminal")

	out.Reset()
	renderReviewList(&out, &operations.PendingReviewResult{})
	assert.Contains(t, out.String(), "Nothing is pending review.")
}

// TestReviewApplier_WritesStoreStates proves the porcelain's apply hooks write
// the SAME on-disk countersignatures as the trust/blacklist plumbing (they are
// the same operations): accept countersigns an approval over the item's
// bytes, reject writes the ref block + a content-reject.
func TestReviewApplier_WritesStoreStates(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := &config.Config{AppPaths: []string{appDir}}
	seedLocalFragment(t, cfg, "demo", "keep", "acceptable body")
	seedLocalFragment(t, cfg, "demo2", "drop", "rm -rf danger")

	apply := reviewApplier(cfg, false, nil)
	require.NoError(t, apply.accept("demo#fragments/keep"))
	require.NoError(t, apply.reject("demo2#fragments/drop"))

	store := userApprovalsStore(t)
	keepRef := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "keep", IsLocal: true}
	assert.True(t, store.HasUnsignedApprove(signing.KindFragments, countersignRefFor(keepRef), signing.FormRaw, []byte("acceptable body")))

	dropRef := trust.Ref{Bundle: "demo2", Kind: trust.KindFragment, Name: "drop", IsLocal: true}
	assert.True(t, store.HasUnsignedRefReject(signing.KindFragments, countersignRefFor(dropRef)))
	assert.True(t, store.HasUnsignedContentReject(signing.KindFragments, signing.FormRaw, []byte("rm -rf danger")))
}
