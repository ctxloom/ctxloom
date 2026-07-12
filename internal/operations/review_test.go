package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// reviewSeedKey is the canonical (remote) bundle ref the review tests seed.
const reviewSeedKey = acmeBundle + "toolkit"

// reviewBundle builds the test bundle: two fragments (one with a distilled
// form), a skill, an MCP server, and a hook — one of every reviewable kind.
func reviewBundle() *bundles.Bundle {
	return &bundles.Bundle{
		Version: "1.0",
		Fragments: map[string]bundles.BundleFragment{
			"solid": {Content: "solid raw body"},
			"dual":  {Content: "dual raw body", Distilled: "dual distilled body"},
		},
		Skills: map[string]bundles.BundleSkill{
			"greet": {Content: "greet body"},
		},
		MCP: map[string]bundles.BundleMCP{
			"pg": {Command: "pg-mcp", Args: []string{"--port", "5432"}, Env: map[string]string{"PGHOST": "localhost", "APP": "x"}},
		},
		Hooks: bundles.BundleHooks{
			PreTool: []bundles.BundleHook{{Type: "command", Command: "echo hi", Matcher: "Bash"}},
		},
	}
}

func reviewLoader(b *bundles.Bundle) *bundles.Loader {
	return bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{reviewSeedKey: b}))
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

	assert.Equal(t, 5, res.Total)
	assert.Equal(t, 0, res.Updates)
	require.Len(t, res.Bundles, 1)
	assert.Equal(t, reviewSeedKey, res.Bundles[0].Ref)
	assert.Equal(t, "acme", res.Bundles[0].Remote, "the registered remote must be named")

	refs := pendingRefs(res)
	for _, want := range []string{
		reviewSeedKey + "#fragments/solid",
		reviewSeedKey + "#fragments/dual",
		reviewSeedKey + "#skills/greet",
		reviewSeedKey + "#mcp/pg",
		reviewSeedKey + "#hooks/pre_tool/0",
	} {
		assert.Equalf(t, ReviewStatusNew, refs[want], "%s must be pending NEW", want)
	}
}

// TestPendingReview_ContentAndRendering: fragments/skills carry the effective
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
		assert.Equal(t, 4, res.Total)
	})

	t.Run("rejected is excluded (already decided, not pending)", func(t *testing.T) {
		fx := newTrustFixture(t)
		fx.rejectRef(trust.Ref{RepoURL: trustRepo, Bundle: "toolkit", Kind: trust.KindPrompt, Name: "greet"})

		res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: newRegistry(t), Loader: reviewLoader(reviewBundle()), FS: afero.NewMemMapFs()})
		require.NoError(t, err)
		assert.NotContains(t, pendingRefs(res), reviewSeedKey+"#skills/greet")
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
		loader := bundles.NewLoader(nil, true, bundles.WithSeededBundles(map[string]*bundles.Bundle{"localb": local}))
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

// TestAcceptReviewItems_BundleAcceptAll drives the accept-all path: every
// pending ref is accepted through the single mutation path; a re-enumeration
// then finds nothing pending. Per-ref failures are reported, never fatal.
func TestAcceptReviewItems_BundleAcceptAll(t *testing.T) {
	fx := newTrustFixture(t)
	fs := afero.NewMemMapFs()
	loader := reviewLoader(reviewBundle())
	registry := newRegistry(t, remoteSpec{name: "acme", url: trustRepo})

	res, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: registry, Loader: loader, FS: fs})
	require.NoError(t, err)
	require.Equal(t, 5, res.Total)

	var refs []string
	for _, b := range res.Bundles {
		for _, it := range b.Items {
			refs = append(refs, it.Ref)
		}
	}
	refs = append(refs, reviewSeedKey+"#fragments/does-not-exist") // one bad ref must not sink the batch

	applied := AcceptReviewItems(nil, AcceptReviewItemsRequest{Refs: refs, Signer: fx.signer, UserStore: fx.user, Loader: loader, FS: fs})
	assert.Len(t, applied.Accepted, 5)
	require.Len(t, applied.Failed, 1)
	assert.Contains(t, applied.Failed, reviewSeedKey+"#fragments/does-not-exist")

	after, err := PendingReview(nil, PendingReviewRequest{UserStore: fx.user, Root: fx.root, Registry: registry, Loader: loader, FS: fs})
	require.NoError(t, err)
	assert.Zero(t, after.Total, "accept-all must clear the pending set")
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
