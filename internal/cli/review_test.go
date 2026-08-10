package cli

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/signing/agentkey"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// TestParseReviewChoice covers the menu parse.
//
// The letters are the CLI's own verbs — [t]rust and [r]eject, the same words
// `ctxloom bundle trust` and `ctxloom bundle reject` are spelled with — so the
// porcelain teaches the plumbing instead of teaching a second vocabulary a
// user then has to translate.
//
// Each bulk form is its verb's UPPERCASE only: they are the widest actions on
// offer, so neither may be reached by case-sloppy typing of the single one.
// Everything else is a skip, including the retired `a`/`A` spellings and the
// empty line, because viewing must never mutate trust.
func TestParseReviewChoice(t *testing.T) {
	cases := map[string]reviewDecision{
		"t":     reviewTrust,
		" t ":   reviewTrust,
		"T":     reviewTrustBundle,
		" T ":   reviewTrustBundle,
		"r":     reviewReject,
		" r ":   reviewReject,
		"R":     reviewRejectBundle,
		" R ":   reviewRejectBundle,
		"q":     reviewQuit,
		"Q":     reviewQuit,
		"s":     reviewSkip,
		"":      reviewSkip,
		"skip":  reviewSkip,
		"trust": reviewSkip, // only single-letter shortcuts act
		"yes":   reviewSkip,
		"junk":  reviewSkip,
		// The retired accept spellings. Muscle memory must land on the SAFE
		// side: a stale `a` skips the item rather than silently approving it.
		"a": reviewSkip,
		"A": reviewSkip,
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
				Ref:       "https://github.com/acme/repo@bundles/one",
				Remote:    "acme",
				Publisher: bundles.ReasonUnsigned,
				Items: []operations.ReviewItem{
					{Ref: "one#fragments/f1", Kind: "fragments", Name: "f1", Status: operations.ReviewStatusNew, CurrentContent: "f1 body"},
					{Ref: "one#commands/s1", Kind: "commands", Name: "s1", Status: operations.ReviewStatusUpdate, CurrentContent: "s1 v2", PreviousContent: "s1 v1"},
					{Ref: "one#mcp/m1", Kind: "mcp", Name: "m1", Status: operations.ReviewStatusNew, Executable: true, CurrentContent: "command: m1\n"},
				},
			},
			{
				Ref:               "https://github.com/acme/repo@bundles/two",
				Publisher:         bundles.ReasonUntrustedSigner,
				SignerFingerprint: "SHA256:qc0G8V6Bhw4mDeLpUEzGmxJmM8LDG1qFCkTgVoMcYpk",
				Items: []operations.ReviewItem{
					{Ref: "two#fragments/f2", Kind: "fragments", Name: "f2", Status: operations.ReviewStatusNew, CurrentContent: "f2 body"},
					{Ref: "two#fragments/f3", Kind: "fragments", Name: "f3", Status: operations.ReviewStatusNew, CurrentContent: "f3 body"},
				},
			},
		},
	}
}

// TestRunReviewWalk_TrustRejectSkip drives one of each single-item action and
// checks tallies, applied refs, and the update diff rendering.
func TestRunReviewWalk_TrustRejectSkip(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	// f1 trust, s1 reject, m1 skip, f2 trust, f3 skip.
	sum := runReviewWalk(&out, scriptedPrompt("t", "r", "s", "t", ""), walkFixture(), rec.funcs())

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

// TestRunReviewWalk_TrustBundle: 'T' trusts the current item and everything
// remaining in the SAME bundle, then the walk moves to the next bundle.
func TestRunReviewWalk_TrustBundle(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	// f1: T (trusts f1, s1, m1 without further prompts), f2: r, f3: s.
	sum := runReviewWalk(&out, scriptedPrompt("T", "r", "s"), walkFixture(), rec.funcs())

	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1"}, rec.accepted)
	assert.Equal(t, []string{"two#fragments/f2"}, rec.rejected)
	assert.Equal(t, 3, sum.accepted)
	assert.Equal(t, 1, sum.rejected)
	assert.Equal(t, 1, sum.skipped)
}

// TestRunReviewWalk_RejectBundle is the bulk form's other direction, and it is
// the one that must be proven: bulk TRUST at least re-gates itself the moment
// any of those bytes change, while every rejection it writes is sticky.
//
// The bulk decision must also stay inside its bundle. A 'R' that ran on to the
// next bundle would reject content the reviewer never saw, permanently.
func TestRunReviewWalk_RejectBundle(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	// f1: R (rejects f1, s1, m1 without further prompts), f2: t, f3: s.
	sum := runReviewWalk(&out, scriptedPrompt("R", "t", "s"), walkFixture(), rec.funcs())

	assert.Equal(t, []string{"one#fragments/f1", "one#commands/s1", "one#mcp/m1"}, rec.rejected)
	assert.Equal(t, []string{"two#fragments/f2"}, rec.accepted,
		"the next bundle is still decided item by item — a bulk answer covers the bundle it was given in, and no more")
	assert.Equal(t, 3, sum.rejected)
	assert.Equal(t, 1, sum.accepted)
	assert.Equal(t, 1, sum.skipped)
}

// TestRunReviewWalk_Quit: 'q' ends the session immediately; nothing after it
// is prompted or mutated, and the remainder stays pending.
func TestRunReviewWalk_Quit(t *testing.T) {
	rec := &recordingApply{}
	var out bytes.Buffer
	sum := runReviewWalk(&out, scriptedPrompt("t", "q"), walkFixture(), rec.funcs())

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
	sum := runReviewWalk(&out, scriptedPrompt("t", "q"), walkFixture(), rec.funcs())

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

// The three publisher states, asserted as the BYTES a user reads rather than as
// a field that was set. "Pending" and "pending because I do not trust who
// signed it" are different diagnoses with different fixes, so each state is
// pinned together with the command it sends the reader to; a rendering that
// distinguished the states but named the same next command would leave the
// diagnosis gap open.
func TestRenderReviewPublisher_ThreeStatesNameTheirOwnNextCommand(t *testing.T) {
	const fingerprint = "SHA256:qc0G8V6Bhw4mDeLpUEzGmxJmM8LDG1qFCkTgVoMcYpk"

	render := func(b operations.ReviewBundle) string {
		var out bytes.Buffer
		renderReviewPublisher(&out, b)
		return out.String()
	}

	assert.Equal(t,
		"  signer:  none — these bytes carry no publisher signature\n"+
			"           Nothing to compare; read the items and decide: ctxloom review\n",
		render(operations.ReviewBundle{Publisher: bundles.ReasonUnsigned}))

	assert.Equal(t,
		"  signer:  untrusted key "+fingerprint+"\n"+
			"           Signed, but by a key this machine does not trust to publish.\n"+
			"           That fingerprint is a string to COMPARE, not a name: confirm it\n"+
			"           with the publisher out of band, then trust the key by principal:\n"+
			"             ctxloom signer trust <principal> --key <key.pub>\n",
		render(operations.ReviewBundle{
			Publisher:         bundles.ReasonUntrustedSigner,
			SignerFingerprint: fingerprint,
		}))

	assert.Equal(t,
		"  signer:  runbooks@acme.example — a key you trust to publish\n"+
			"           Read the items and decide: ctxloom review\n",
		render(operations.ReviewBundle{
			Publisher: bundles.ReasonTrustedSigner,
			Signer:    "runbooks@acme.example",
		}))
}

// A fingerprint shown in the surface that decides whether to admit content must
// never read as an ENDORSEMENT or as an identity. These are the properties that
// keep it from doing so, asserted on the rendered bytes:
//   - the word "untrusted" is adjacent to the key, not buried in prose below it;
//   - the fingerprint is never paired with a principal or any other name;
//   - the out-of-band comparison is stated BEFORE the trust command, so the
//     command reads as the second step and not the offered action.
func TestRenderReviewPublisher_UntrustedKeyReadsAsAWarningNotAnIdentity(t *testing.T) {
	const fingerprint = "SHA256:qc0G8V6Bhw4mDeLpUEzGmxJmM8LDG1qFCkTgVoMcYpk"
	var out bytes.Buffer
	renderReviewPublisher(&out, operations.ReviewBundle{
		Publisher:         bundles.ReasonUntrustedSigner,
		SignerFingerprint: fingerprint,
		// A principal MUST NOT be rendered for this state even if one is
		// somehow present: naming a party nothing verified is the failure.
		Signer: "runbooks@acme.example",
	})
	text := out.String()

	assert.Contains(t, text, "untrusted key "+fingerprint,
		"the verdict must sit on the same line as the key it is about")
	assert.NotContains(t, text, "runbooks@acme.example",
		"a fingerprint must never be presented next to a principal this machine has not verified")
	assert.NotContains(t, text, "trust to publish\n           "+fingerprint,
		"the key must not be re-rendered as a bare identity line")
	assert.Less(t,
		strings.Index(text, "out of band"),
		strings.Index(text, "ctxloom signer trust"),
		"the comparison must be stated before the command that acts on it")
}

// TestReviewApplier_WritesStoreStates proves the porcelain's apply hooks write
// the SAME on-disk countersignatures as the trust/blacklist plumbing (they are
// the same operations): accept countersigns an approval over the item's
// bytes, reject writes the ref block + a content-reject.
func TestReviewApplier_WritesStoreStates(t *testing.T) {
	appDir := t.TempDir()
	neutralizeRefresh(t)
	noAgentEnv(t)
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
	seedLocalFragment(t, cfg, "demo", "keep", "acceptable body")
	seedLocalFragment(t, cfg, "demo2", "drop", "rm -rf danger")

	apply := reviewApplier(cfg, false, nil)
	require.NoError(t, apply.accept("demo#fragments/keep"))
	require.NoError(t, apply.reject("demo2#fragments/drop"))

	store := userApprovalsStore(t)
	keepRef := trust.Ref{Bundle: "demo", Kind: trust.KindFragment, Name: "keep", IsLocal: true}
	assert.True(t, store.HasUnsignedApprove(countersignRefFor(keepRef), signing.AttestFragmentRaw, []byte("acceptable body")))

	dropRef := trust.Ref{Bundle: "demo2", Kind: trust.KindFragment, Name: "drop", IsLocal: true}
	assert.True(t, store.HasUnsignedRefReject(countersignRefFor(dropRef)))
	assert.True(t, store.HasUnsignedContentReject(signing.AttestFragmentRaw, []byte("rm -rf danger")))
}

// TestPrintReviewItem_ShowsBothCountersignedForms closes a gap: an
// approval countersigns BOTH forms of a distillable item, so the reviewer must
// see both. Showing only the effective form meant approving a fragment you read
// in raw also blessed distilled bytes you never saw.
func TestPrintReviewItem_ShowsBothCountersignedForms(t *testing.T) {
	var out bytes.Buffer
	printReviewItem(&out, 1, 1, operations.ReviewItem{
		Ref: "b#fragments/x", Kind: "fragments", Name: "x",
		Status:           operations.ReviewStatusNew,
		CurrentContent:   "the distilled body",
		CurrentForm:      "distilled",
		AlternateContent: "the raw body",
		AlternateForm:    "raw",
	})
	text := out.String()
	assert.Contains(t, text, "the distilled body")
	assert.Contains(t, text, "the raw body",
		"the second countersigned form must be shown — approving covers it too")
	assert.Contains(t, text, "also covered by this approval")
}

// TestPrintReviewItem_UpdateShowsAlternateForm proves the second form survives
// the UPDATE/diff branch, which used to `return` early after the diff.
func TestPrintReviewItem_UpdateShowsAlternateForm(t *testing.T) {
	var out bytes.Buffer
	printReviewItem(&out, 1, 1, operations.ReviewItem{
		Ref: "b#fragments/x", Kind: "fragments", Name: "x",
		Status:           operations.ReviewStatusUpdate,
		PreviousContent:  "old distilled body\n",
		CurrentContent:   "new distilled body\n",
		CurrentForm:      "distilled",
		AlternateContent: "the raw body",
		AlternateForm:    "raw",
	})
	text := out.String()
	assert.Contains(t, text, "new distilled body")
	assert.Contains(t, text, "the raw body")
}

// TestResolveReviewSigner_HonoursSignKeyConfig closes a gap: `ctxloom
// sign` merges cfg.SignKey() into the discovery chain's explicit-key slot but
// `ctxloom review` passed "", so approve never consulted sign.key. With
// several ssh-agent identities and no git user.signingkey, sign worked and the
// doctor SIGNKEY-k1 check reported "ok" — while review still failed ambiguous
// and could not countersign. The doctor check's headline reason is approve, so
// the OK was false.
func TestResolveReviewSigner_HonoursSignKeyConfig(t *testing.T) {
	_, wantedPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	_, otherPriv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	kr := agent.NewKeyring()
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: otherPriv, Comment: "other@example.com"}))
	require.NoError(t, kr.Add(agent.AddedKey{PrivateKey: wantedPriv, Comment: "ben@abbitt.me"}))

	discoverer := &agentkey.Discoverer{
		GitConfig: func(ctx context.Context, dir, key string) (string, bool, error) { return "", false, nil },
		DialAgent: func() (agent.Agent, error) { return kr, nil },
		ReadFile:  func(path string) ([]byte, error) { return nil, assert.AnError },
	}

	t.Run("sign.key disambiguates a multi-identity agent", func(t *testing.T) {
		signer, unsigned, err := resolveReviewSigner(context.Background(), discoverer, "ben@abbitt", false)
		require.NoError(t, err)
		require.False(t, unsigned, "sign.key resolves a key, so review must not degrade to unsigned")
		wanted, err := ssh.NewSignerFromSigner(wantedPriv)
		require.NoError(t, err)
		assert.Equal(t, ssh.FingerprintSHA256(wanted.PublicKey()),
			ssh.FingerprintSHA256(signer.PublicKey()),
			"review must countersign with the SAME key `ctxloom sign` would use")
	})

	t.Run("without sign.key a multi-identity agent stays ambiguous", func(t *testing.T) {
		_, unsigned, err := resolveReviewSigner(context.Background(), discoverer, "", false)
		require.NoError(t, err)
		assert.True(t, unsigned, "no explicit key + ambiguous agent still degrades to the unsigned path")
	})
}

// TestPrintReviewItem_UnchangedUpdateSaysSo pins the reachable half of a fix:
// an UPDATE whose diff comes out EMPTY silently fell through to the
// full-content display with no explanation at all. That is not a corner case —
// operations.buildReviewItem labels an item UPDATE whenever a PRIOR approve
// entry exists, including when the bytes are identical and the item is pending
// only because its approval record was superseded (a countersign-contract
// bump). The reviewer is then shown the entire body of something they already
// approved, with nothing saying why. Every other fall-through to full content
// in this function names its reason; this one must too.
func TestPrintReviewItem_UnchangedUpdateSaysSo(t *testing.T) {
	const body = "line one\nline two\n"
	var out bytes.Buffer
	printReviewItem(&out, 1, 1, operations.ReviewItem{
		Ref: "b#fragments/x", Kind: "fragments", Name: "x",
		Status:          operations.ReviewStatusUpdate,
		PreviousContent: body,
		CurrentContent:  body,
	})
	got := out.String()
	assert.Contains(t, got, "unchanged since it was approved",
		"an update with no delta must say why the full content is being shown")
	assert.Contains(t, got, "line one", "the content itself is still shown")
}

// TestPrintReviewItem_ChangedUpdateStillDiffs is the discriminator: a real
// delta must still render as a diff and must NOT carry the unchanged notice.
func TestPrintReviewItem_ChangedUpdateStillDiffs(t *testing.T) {
	var out bytes.Buffer
	printReviewItem(&out, 1, 1, operations.ReviewItem{
		Ref: "b#fragments/x", Kind: "fragments", Name: "x",
		Status:          operations.ReviewStatusUpdate,
		PreviousContent: "line one\nline two\n",
		CurrentContent:  "line one\nline TWO\n",
	})
	got := out.String()
	assert.Contains(t, got, "--- accepted", "a real delta renders as a unified diff")
	assert.Contains(t, got, "+line TWO")
	assert.NotContains(t, got, "unchanged since it was approved")
}

// TestReviewWantsListing pins a fix: `ctxloom review --format json` on a TTY
// took the interactive countersigning walk: --format was accepted, never read,
// and nothing rendered through emit — so the human was prompted through an
// approval session and the invocation only failed the format-was-honored guard
// afterwards, with the countersignatures already written. An invocation that
// asked for a value it can parse must get the pending table instead.
func TestReviewWantsListing(t *testing.T) {
	newCmd := func(format string) *cobra.Command {
		c := &cobra.Command{Use: "review"}
		c.Flags().String("format", "text", "")
		if format != "" {
			require.NoError(t, c.Flags().Set("format", format))
		}
		return c
	}

	for _, tc := range []struct {
		name        string
		format      string
		listFlag    bool
		interactive bool
		want        bool
	}{
		{"tty, default format, walks", "", false, true, false},
		{"tty, explicit text, walks", "text", false, true, false},
		{"tty, --list, lists", "", true, true, true},
		{"no tty, lists", "", false, false, true},
		{"tty, --format json, lists", "json", false, true, true},
		{"tty, --format yaml, lists", "yaml", false, true, true},
		{"tty, --format markdown, lists", "markdown", false, true, true},
		{"tty, unparseable format, lists", "xml", false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := reviewWantsListing(newCmd(tc.format), tc.listFlag, tc.interactive)
			assert.Equal(t, tc.want, got)
		})
	}
}

// TestPrintReviewItem_AllArms characterizes every arm of printReviewItem's
// body rendering before it is split, so the split is provably
// behaviour-preserving (a pure complexity reduction: nothing here
// can go red by definition).
func TestPrintReviewItem_AllArms(t *testing.T) {
	for _, tc := range []struct {
		name    string
		item    operations.ReviewItem
		want    []string
		notWant []string
	}{
		{
			name: "new item shows full content",
			item: operations.ReviewItem{Kind: "fragments", Name: "x", Status: operations.ReviewStatusNew, CurrentContent: "brand new body"},
			want: []string{"(NEW)", "brand new body"},
			notWant: []string{"--- accepted", "no snapshot of the previously accepted content",
				"unchanged since it was approved", "no differences could be rendered"},
		},
		{
			name:    "update with a delta diffs",
			item:    operations.ReviewItem{Kind: "fragments", Name: "x", Status: operations.ReviewStatusUpdate, PreviousContent: "a\n", CurrentContent: "b\n"},
			want:    []string{"UPDATE", "--- accepted", "+++ incoming", "+b"},
			notWant: []string{"unchanged since it was approved", "no snapshot of the previously accepted content"},
		},
		{
			name:    "update with no snapshot says so",
			item:    operations.ReviewItem{Kind: "fragments", Name: "x", Status: operations.ReviewStatusUpdate, CurrentContent: "full body"},
			want:    []string{"no snapshot of the previously accepted content", "full body"},
			notWant: []string{"--- accepted"},
		},
		{
			name:    "executable update with no snapshot stays quiet",
			item:    operations.ReviewItem{Kind: "mcp", Name: "srv", Status: operations.ReviewStatusUpdate, Executable: true, CurrentContent: "npx thing"},
			want:    []string{"npx thing"},
			notWant: []string{"no snapshot of the previously accepted content", "--- accepted"},
		},
		{
			name:    "empty content renders the empty marker",
			item:    operations.ReviewItem{Kind: "fragments", Name: "x", Status: operations.ReviewStatusNew},
			want:    []string{"(empty)"},
			notWant: []string{"--- accepted"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			printReviewItem(&out, 2, 7, tc.item)
			got := out.String()
			assert.Contains(t, got, "[2/7]")
			for _, w := range tc.want {
				assert.Contains(t, got, w)
			}
			for _, n := range tc.notWant {
				assert.NotContains(t, got, n)
			}
		})
	}
}
