package operations

import (
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// reviewSeedKey is the canonical (remote) bundle ref the review tests seed.
const reviewSeedKey = acmeBundle + "toolkit"

// reviewBundle builds the test bundle: two fragments (one with a distilled
// form), a command, an MCP server, a hook, and a skill package — one of every
// reviewable kind.
func reviewBundle() *bundles.Bundle {
	return &bundles.Bundle{
		Version: "1.0",
		Fragments: map[string]bundles.BundleFragment{
			"solid": {Content: "solid raw body"},
			"dual":  {Content: "dual raw body", Distilled: "dual distilled body"},
		},
		Commands: map[string]bundles.BundleCommand{
			"greet": {Content: "greet body"},
		},
		MCP: map[string]bundles.BundleMCP{
			"pg": {Command: "pg-mcp", Args: []string{"--port", "5432"}, Env: map[string]string{"PGHOST": "localhost", "APP": "x"}},
		},
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{{Type: "command", Command: "echo hi", Matcher: "Bash"}},
		},
		Skills: map[string]bundles.BundleSkill{
			"humanize": {Files: map[string]bundles.SkillFileMeta{
				"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"},
				"scripts/run.sh": {SHA256: "sha256:script1", Mode: "0755"},
			}},
		},
	}
}

func reviewLoader(b *bundles.Bundle) *bundles.Loader {
	return bundles.NewLoader(nil, bundles.WithSeededBundles(map[string]*bundles.Bundle{reviewSeedKey: b}))
}

// pendingRefs flattens an enumeration to "ref:status" strings for compact
// assertions.
func pendingRefs(res *PendingReviewResult) map[string]string {
	out := map[string]string{}
	for _, b := range res.Bundles {
		for _, it := range b.Items {
			out[it.Ref] = it.Status
		}
	}
	return out
}

// TestPendingReview_FreshRecordsAllPending: with nothing ever approved or
// rejected, every item of an untrusted remote bundle is pending NEW — all
// four kinds, in one bundle group, with the registered remote named in the
// header.
func TestPendingReview_FreshRecordsAllPending(t *testing.T) {
	fx := newTrustFixture(t)
	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t, remoteSpec{name: "acme", url: trustRepo}),
		Loader:   reviewLoader(reviewBundle()),
		FS:       afero.NewMemMapFs(),
	})
	require.NoError(t, err)

	assert.Equal(t, 6, res.Total)
	assert.Equal(t, 0, res.Updates)
	require.Len(t, res.Bundles, 1)
	assert.Equal(t, reviewSeedKey, res.Bundles[0].Ref)
	assert.Equal(t, "acme", res.Bundles[0].Remote, "the registered remote must be named")

	refs := pendingRefs(res)
	for _, want := range []string{
		reviewSeedKey + "#fragments/solid",
		reviewSeedKey + "#fragments/dual",
		reviewSeedKey + "#commands/greet",
		reviewSeedKey + "#mcp/pg",
		reviewSeedKey + "#hooks/pre_tool/0",
		reviewSeedKey + "#skills/humanize",
	} {
		assert.Equalf(t, ReviewStatusNew, refs[want], "%s must be pending NEW", want)
	}
}

// TestPendingReview_ContentAndRendering: fragments/commands carry the effective
// content that would be exposed (distilled when one exists); executables carry
// the rendered what-it-runs surface.
func TestPendingReview_ContentAndRendering(t *testing.T) {
	fx := newTrustFixture(t)
	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t),
		Loader:   reviewLoader(reviewBundle()),
		FS:       afero.NewMemMapFs(),
	})
	require.NoError(t, err)

	byRef := map[string]ReviewItem{}
	for _, b := range res.Bundles {
		for _, it := range b.Items {
			byRef[it.Ref] = it
		}
	}

	assert.Equal(t, "dual distilled body", byRef[reviewSeedKey+"#fragments/dual"].CurrentContent,
		"review must show the effective (distilled-preferred) form — the bytes that would be exposed")
	assert.Equal(t, "solid raw body", byRef[reviewSeedKey+"#fragments/solid"].CurrentContent)

	mcp := byRef[reviewSeedKey+"#mcp/pg"]
	assert.True(t, mcp.Executable)
	assert.Contains(t, mcp.CurrentContent, "command: pg-mcp")
	assert.Contains(t, mcp.CurrentContent, "args:    --port 5432")
	assert.Contains(t, mcp.CurrentContent, "APP=x")
	assert.Contains(t, mcp.CurrentContent, "PGHOST=localhost")

	hook := byRef[reviewSeedKey+"#hooks/pre_tool/0"]
	assert.True(t, hook.Executable)
	assert.Contains(t, hook.CurrentContent, "event:   pre_tool")
	assert.Contains(t, hook.CurrentContent, "matcher: Bash")
	assert.Contains(t, hook.CurrentContent, "command: echo hi")

	// A skill renders as a per-file TREE listing (path, hash, mode), NOT
	// a single blob — the entire reason it is stored as a directory rather
	// than inline content. It is deliberately non-executable (Executable ==
	// false) so it flows through the same content-snapshot/diff machinery as
	// fragments/commands, unlike mcp/hooks.
	skill := byRef[reviewSeedKey+"#skills/humanize"]
	assert.False(t, skill.Executable, "a skill is a reviewable TREE, not an opaque executable surface")
	assert.Contains(t, skill.CurrentContent, "SKILL.md")
	assert.Contains(t, skill.CurrentContent, "sha256:skillmd1")
	assert.Contains(t, skill.CurrentContent, "mode:0644")
	assert.Contains(t, skill.CurrentContent, "scripts/run.sh")
	assert.Contains(t, skill.CurrentContent, "sha256:script1")
	assert.Contains(t, skill.CurrentContent, "mode:0755")
}

// TestPendingReview_DecidedAndExemptExcluded: approved-at-current-bytes,
// rejected (ref state), content-rejected, trusted sources, and local bundles
// never appear in the walk.
func TestPendingReview_DecidedAndExemptExcluded(t *testing.T) {
	t.Run("approved at current bytes is excluded", func(t *testing.T) {
		fx := newTrustFixture(t)
		loader := reviewLoader(reviewBundle())
		fs := afero.NewMemMapFs()
		_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Signer: fx.signer, Loader: loader, FS: fs})
		require.NoError(t, err)

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: loader, FS: fs})
		require.NoError(t, err)
		assert.NotContains(t, pendingRefs(res), reviewSeedKey+"#fragments/solid")
		assert.Equal(t, 5, res.Total)
	})

	t.Run("rejected is excluded (already decided, not pending)", func(t *testing.T) {
		fx := newTrustFixture(t)
		fx.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindPrompt, Name: "greet"})

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(reviewBundle()), FS: afero.NewMemMapFs()})
		require.NoError(t, err)
		assert.NotContains(t, pendingRefs(res), reviewSeedKey+"#commands/greet")
	})

	t.Run("content-rejected is excluded even under a fresh ref", func(t *testing.T) {
		fx := newTrustFixture(t)
		b := reviewBundle()
		// Content-reject "solid"'s bytes under an UNRELATED ref — the
		// rejection is deliberately ref-omitted (spec §5.3), so it must
		// still deny "solid" wherever those exact bytes appear.
		fx.rejectContent(trust.KindFragment, signing.FormRaw, []byte("solid raw body"))

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(b), FS: afero.NewMemMapFs()})
		require.NoError(t, err)
		assert.NotContains(t, pendingRefs(res), reviewSeedKey+"#fragments/solid")
	})

	t.Run("trusted publisher is exempt — nothing pending", func(t *testing.T) {
		// A bundle carrying a verified publisher signer is allowed at step 4, so
		// review shows nothing for it (an item from a trusted publisher is not
		// pending and must never be presented for review as though it were).
		signed := reviewBundle()
		signed.StampSigner(trustedPublisher)
		fx := newTrustFixture(t)
		res, err := PendingReview(nil, PendingReviewRequest{
			UserStore: fx.user, Root: fx.root,
			Registry: newRegistry(t),
			Loader:   reviewLoader(signed),
			FS:       afero.NewMemMapFs(),
		})
		require.NoError(t, err)
		assert.Zero(t, res.Total)
		assert.Empty(t, res.Bundles)
	})

	t.Run("local bundle is exempt — nothing pending", func(t *testing.T) {
		local := &bundles.Bundle{
			Version:   "1.0",
			Fragments: map[string]bundles.BundleFragment{"x": {Content: "project-authored"}},
		}
		loader := bundles.NewLoader(nil, bundles.WithSeededBundles(map[string]*bundles.Bundle{"localb": local}))
		fx := newTrustFixture(t)
		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: loader, FS: afero.NewMemMapFs()})
		require.NoError(t, err)
		assert.Zero(t, res.Total)
	})
}

// TestPendingReview_UpdateWithDiffBase drives the full update cycle: approve →
// upstream edit → the item returns as UPDATE carrying the previously-approved
// effective-form snapshot as the diff base — including the form selection
// (distilled preferred when one exists, raw otherwise).
func TestPendingReview_UpdateWithDiffBase(t *testing.T) {
	t.Run("distilled-form item diffs against the approved distilled text", func(t *testing.T) {
		fx := newTrustFixture(t)
		fs := afero.NewMemMapFs()
		_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/dual", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
		require.NoError(t, err)

		// Upstream edits the distilled form.
		edited := reviewBundle()
		dual := edited.Fragments["dual"]
		dual.Distilled = "dual distilled body v2"
		edited.Fragments["dual"] = dual

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(edited), FS: fs})
		require.NoError(t, err)

		refs := pendingRefs(res)
		require.Equal(t, ReviewStatusUpdate, refs[reviewSeedKey+"#fragments/dual"], "an approved item whose bytes changed is an UPDATE")
		assert.Positive(t, res.Updates)

		var item ReviewItem
		for _, b := range res.Bundles {
			for _, it := range b.Items {
				if it.Ref == reviewSeedKey+"#fragments/dual" {
					item = it
				}
			}
		}
		assert.Equal(t, "dual distilled body", item.PreviousContent,
			"the diff base must be the previously approved DISTILLED text (the effective form)")
		assert.Equal(t, "dual distilled body v2", item.CurrentContent)
	})

	t.Run("raw-form item diffs against the approved raw text", func(t *testing.T) {
		fx := newTrustFixture(t)
		fs := afero.NewMemMapFs()
		_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
		require.NoError(t, err)

		edited := reviewBundle()
		solid := edited.Fragments["solid"]
		solid.Content = "solid raw body v2"
		edited.Fragments["solid"] = solid

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(edited), FS: fs})
		require.NoError(t, err)

		for _, b := range res.Bundles {
			for _, it := range b.Items {
				if it.Ref == reviewSeedKey+"#fragments/solid" {
					assert.Equal(t, ReviewStatusUpdate, it.Status)
					assert.Equal(t, "solid raw body", it.PreviousContent)
				}
			}
		}
	})

	t.Run("skill package diffs per-file — editing one script re-triggers review as a tree diff", func(t *testing.T) {
		// This is the entire reason a skill is stored as a directory of
		// files rather than a single blob (skill/command split plan §3.1):
		// changing ONE file — here, scripts/run.sh's hash and mode both flip
		// — must show up as exactly that one line changing in the rendered
		// tree listing, with every untouched file's line unchanged.
		fx := newTrustFixture(t)
		fs := afero.NewMemMapFs()
		_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#skills/humanize", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
		require.NoError(t, err)

		edited := reviewBundle()
		humanize := edited.Skills["humanize"]
		humanize.Files = map[string]bundles.SkillFileMeta{
			"SKILL.md":       {SHA256: "sha256:skillmd1", Mode: "0644"}, // unchanged
			"scripts/run.sh": {SHA256: "sha256:script2-tampered", Mode: "0644"},
		}
		edited.Skills["humanize"] = humanize

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(edited), FS: fs})
		require.NoError(t, err)

		var item ReviewItem
		for _, b := range res.Bundles {
			for _, it := range b.Items {
				if it.Ref == reviewSeedKey+"#skills/humanize" {
					item = it
				}
			}
		}
		assert.Equal(t, ReviewStatusUpdate, item.Status, "editing any one file in the tree must re-trigger review")
		assert.Contains(t, item.PreviousContent, "sha256:script1")
		assert.Contains(t, item.PreviousContent, "mode:0755")
		assert.Contains(t, item.CurrentContent, "sha256:script2-tampered")
		assert.Contains(t, item.CurrentContent, "mode:0644")
		// The untouched SKILL.md line is identical in both — a per-file diff
		// (not a "the whole skill changed" bookkeeping bit) is what lets a
		// human see exactly which file moved.
		assert.Contains(t, item.PreviousContent, "SKILL.md  sha256:skillmd1  mode:0644")
		assert.Contains(t, item.CurrentContent, "SKILL.md  sha256:skillmd1  mode:0644")
	})

	t.Run("missing snapshot degrades to full content, never an error", func(t *testing.T) {
		// A prior approval was recorded (index + snapshot), but the snapshot
		// object was subsequently lost (e.g. cache pruning) while the index
		// entry survives.
		fx := newTrustFixture(t)
		fs := afero.NewMemMapFs()
		_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
		require.NoError(t, err)
		require.NoError(t, fs.RemoveAll(".ctxloom/cache/trust/objects"))

		edited := reviewBundle()
		solid := edited.Fragments["solid"]
		solid.Content = "solid raw body v2"
		edited.Fragments["solid"] = solid

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(edited), FS: fs})
		require.NoError(t, err)

		refs := pendingRefs(res)
		require.Equal(t, ReviewStatusUpdate, refs[reviewSeedKey+"#fragments/solid"])
		for _, b := range res.Bundles {
			for _, it := range b.Items {
				if it.Ref == reviewSeedKey+"#fragments/solid" {
					assert.Empty(t, it.PreviousContent, "no snapshot → empty diff base (full-content display)")
					assert.Equal(t, "solid raw body v2", it.CurrentContent)
				}
			}
		}
	})
}

// TestSnapshotRoundTrip pins the snapshot store primitives: content-addressed
// write/read under the cache dir with a filename-safe encoding of the hash.
func TestSnapshotRoundTrip(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeTrustSnapshot(fs, ".ctxloom", "sha256:abc123", []byte("the accepted body"))

	got, ok := readTrustSnapshot(fs, ".ctxloom", "sha256:abc123")
	require.True(t, ok)
	assert.Equal(t, "the accepted body", got)

	// The object file lives under cache/trust/objects with ':' made portable.
	exists, err := afero.Exists(fs, ".ctxloom/cache/trust/objects/sha256-abc123")
	require.NoError(t, err)
	assert.True(t, exists)

	_, ok = readTrustSnapshot(fs, ".ctxloom", "sha256:missing")
	assert.False(t, ok, "a missing snapshot reads as absent, never an error")
	_, ok = readTrustSnapshot(fs, ".ctxloom", "")
	assert.False(t, ok, "an empty hash slot never resolves a snapshot")
}

// TestSetItemTrust_WritesSnapshots proves the plumbing approval path writes
// BOTH form snapshots, keyed by the payload hash of each form's bytes.
func TestSetItemTrust_WritesSnapshots(t *testing.T) {
	fx := newTrustFixture(t)
	fs := afero.NewMemMapFs()
	res, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/dual", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
	require.NoError(t, err)
	assert.Equal(t, "approved", res.Status)

	rawHash := bundles.HashPayload([]byte("dual raw body"))
	distilledHash := bundles.HashPayload([]byte("dual distilled body"))

	raw, ok := readTrustSnapshot(fs, ".ctxloom", rawHash)
	require.True(t, ok, "raw snapshot must exist under the raw payload hash")
	assert.Equal(t, "dual raw body", raw)

	distilled, ok := readTrustSnapshot(fs, ".ctxloom", distilledHash)
	require.True(t, ok, "distilled snapshot must exist under the distilled payload hash")
	assert.Equal(t, "dual distilled body", distilled)
}

// TestSetItemTrust_NoSnapshotForExecutables: executables are never snapshotted
// (review always renders their full surface), so approving an MCP server
// leaves the object store empty.
func TestSetItemTrust_NoSnapshotForExecutables(t *testing.T) {
	fx := newTrustFixture(t)
	fs := afero.NewMemMapFs()
	_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#mcp/pg", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
	require.NoError(t, err)

	exists, err := afero.DirExists(fs, ".ctxloom/cache/trust/objects")
	require.NoError(t, err)
	assert.False(t, exists, "executable approvals must not write snapshots")
}

// TestReviewItem_UpdateVsNewAfterPartialDecisions: mixed states in one bundle
// resolve independently — one approved-then-edited (update), one untouched
// (new), one rejected (absent).
func TestReviewItem_UpdateVsNewAfterPartialDecisions(t *testing.T) {
	fx := newTrustFixture(t)
	fs := afero.NewMemMapFs()
	_, err := SetItemTrust(nil, SetItemTrustRequest{Ref: reviewSeedKey + "#fragments/solid", UserStore: fx.user, Signer: fx.signer, Loader: reviewLoader(reviewBundle()), FS: fs})
	require.NoError(t, err)
	fx.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindMCP, Name: "pg"})

	edited := reviewBundle()
	solid := edited.Fragments["solid"]
	solid.Content = "solid raw body v2"
	edited.Fragments["solid"] = solid

	res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(edited), FS: fs})
	require.NoError(t, err)

	refs := pendingRefs(res)
	assert.Equal(t, ReviewStatusUpdate, refs[reviewSeedKey+"#fragments/solid"])
	assert.Equal(t, ReviewStatusNew, refs[reviewSeedKey+"#fragments/dual"])
	assert.NotContains(t, refs, reviewSeedKey+"#mcp/pg", "rejected items are decided, not pending")
	assert.Equal(t, 1, res.Updates)
}

// TestPendingReview_DualFormExposesBothForms closes the form-flip re-gate
// hole: SetItemTrust countersigns BOTH the raw and the distilled form
// of a distillable item, so review must hand the reviewer BOTH sets of bytes.
// Showing only the effective form let an approval bless LLM-written distilled
// bytes the human never read; flipping use_distilled then served them without
// re-gating.
func TestPendingReview_DualFormExposesBothForms(t *testing.T) {
	fx := newTrustFixture(t)
	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t),
		Loader:   reviewLoader(reviewBundle()),
		FS:       afero.NewMemMapFs(),
	})
	require.NoError(t, err)

	byRef := map[string]ReviewItem{}
	for _, b := range res.Bundles {
		for _, it := range b.Items {
			byRef[it.Ref] = it
		}
	}

	dual := byRef[reviewSeedKey+"#fragments/dual"]
	assert.Equal(t, "dual distilled body", dual.CurrentContent)
	assert.Equal(t, string(bundles.FormDistilled), dual.CurrentForm)
	assert.Equal(t, "dual raw body", dual.AlternateContent,
		"the raw form is countersigned by the same approval, so it must be shown too")
	assert.Equal(t, string(bundles.FormRaw), dual.AlternateForm)

	solid := byRef[reviewSeedKey+"#fragments/solid"]
	assert.Equal(t, "solid raw body", solid.CurrentContent)
	assert.Empty(t, solid.AlternateContent, "a single-form item has no second countersigned form")
}

// TestPendingReview_UnreadableSkillIsWarned is a regression guard:
// pendingItems dropped an MCP server, hook, or skill whose
// ContentPayload()/EffectiveManifest() failed via a bare `continue`, with NO
// warning — unlike classify's structurally identical unaddressable-item case,
// which does warn. A manifest-less skill (no authored Files) whose on-disk
// tree cannot be parsed (no SKILL.md at the resolved directory) is the one
// branch of the four bare continues in pendingItems that is reachable with
// realistic bundle data: BundleMCP/BundleHook.ContentPayload() encode only
// strings/slices/maps and cannot fail via json.Marshal (see
// BundleMCP.ComputeContentHash's own "Unreachable" comment), and the skill's
// own ContentPayload() call is never reached once EffectiveManifest itself
// has already failed. Those three got the identical warn-before-continue
// treatment for consistency but have no failure this suite can force.
func TestPendingReview_UnreadableSkillIsWarned(t *testing.T) {
	fx := newTrustFixture(t)
	b := &bundles.Bundle{
		Version: "1.0",
		Path:    "seed/broken-skills.yaml", // gives FSDir() a real directory
		Skills: map[string]bundles.BundleSkill{
			"ghost": {}, // manifest-less: no Files, and no SKILL.md on the fs below
		},
	}

	var buf strings.Builder
	restore := clidiag.SetSink(&buf)
	defer restore()

	res, err := PendingReview(nil, PendingReviewRequest{
		UserStore: fx.user, Root: fx.root,
		Registry: newRegistry(t),
		Loader:   reviewLoader(b),
		FS:       afero.NewMemMapFs(), // empty: no SKILL.md anywhere
	})
	require.NoError(t, err)

	assert.Empty(t, pendingRefs(res), "an unparseable skill must still be withheld from review")
	assert.Contains(t, buf.String(), "ghost",
		"an EffectiveManifest failure must be warned like classify's structurally identical case, not silently dropped")
}

// TestRenderMCPSurface_EmptyServerShowsMarker is a regression guard: an MCP
// server with no command, args, env, or install text used
// to render as just "command: \n" — a reviewer approving it saw nothing
// wrong-looking rather than being told the surface is degenerate. The
// countersignature still binds ContentPayload()'s real bytes regardless (this
// is a display-only fix), but the human must be told they are looking at an
// empty surface.
func TestRenderMCPSurface_EmptyServerShowsMarker(t *testing.T) {
	rendered := renderMCPSurface(bundles.BundleMCP{})
	assert.Contains(t, rendered, "nothing to display",
		"a fully empty MCP surface must say so explicitly, not just print a blank command line")
}

// TestRenderSkillSurface_EmptyManifestShowsMarker is a regression guard:
// renderSkillSurface returned "" outright for a skill whose
// (effective) manifest has zero file entries.
func TestRenderSkillSurface_EmptyManifestShowsMarker(t *testing.T) {
	rendered := renderSkillSurface(bundles.SkillManifest{})
	assert.Contains(t, rendered, "nothing to display",
		"an empty skill manifest must say so explicitly rather than rendering an empty string")
}

// TestRenderHookSurface_NoCommandOrPromptShowsMarker is a regression guard:
// a hook with neither a command nor a prompt executes nothing,
// but used to render only its event/matcher/type header with no signal that
// nothing actually runs.
func TestRenderHookSurface_NoCommandOrPromptShowsMarker(t *testing.T) {
	rendered := renderHookSurface(bundles.HookEntry{Event: "pre_tool", Hook: bundles.BundleHook{Matcher: "Bash"}})
	assert.Contains(t, rendered, "nothing to display",
		"a hook with no command and no prompt must say so explicitly")
}
