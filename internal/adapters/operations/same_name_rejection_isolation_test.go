package operations

import (
	"context"
	"errors"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The name a project bundle and a companion loadout both answer to, the
// fragment they both ship, and the canonical URI each is addressed by. The
// bare name is deliberately absent: it names two bundles here and resolves to
// neither.
const (
	sharedBundleName     = "isolation"
	sharedFragmentName   = "isolation-axes"
	projectFragmentRef   = "ctxloom+local:" + sharedBundleName + "#fragments/" + sharedFragmentName
	companionFragmentRef = "ctxloom+companion:" + sharedBundleName + "#fragments/" + sharedFragmentName
	// companionSharedFragmentBody is the companion copy's bytes.
	companionSharedFragmentBody = "COMPANION-ISOLATION-BODY-7c2e"
)

// sameNameLoader composes two of the readers production composes — project
// and companion — over a project bundle called "isolation" whose
// "isolation-axes" fragment carries projectBody, beside a companion loadout of
// the same name whose copy carries companionSharedFragmentBody.
//
// Neither displaces the other. They are keyed by WHERE each was read, so both
// are in the resolved set and both are reachable through the LOADER route,
// each under its own canonical URI. That is the whole point of this file — two
// items of one declared name, from two sources, live in one session at once.
func sameNameLoader(t *testing.T, projectBody string) *bundles.Loader {
	t.Helper()
	fs := afero.NewMemMapFs()
	bundletree.WriteBundle(t, fs, paths.BundlesLayoutRoot("/bundles", paths.LayoutV2), sharedBundleName, &bundles.Bundle{
		Name: sharedBundleName,
		Fragments: map[string]bundles.BundleFragment{
			sharedFragmentName: {
				ItemBody: bundles.ItemBody{
					Content: projectBody,
				},
			},
		},
	})
	probe := func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{{
			Bin: sharedBundleName,
			Document: testsupport.RunLoadout("version: 1.0.0\nfragments:\n  " + sharedFragmentName +
				":\n    content: " + companionSharedFragmentBody + "\n"),
		}}}, nil
	}
	return bundles.NewLoader(
		bundles.NewProjectReader(fs, []string{"/bundles"}),
		bundles.NewCompanionReader(probe),
	)
}

// trustRefOf is the trust.Ref one copy gates under, taken from the item the
// loader actually produces rather than spelled by hand — a hand-spelled ref
// would keep passing if the loader started minting a different one, which is
// the failure this whole file is about.
func trustRefOf(t *testing.T, loader *bundles.Loader, ask string) trust.Ref {
	t.Helper()
	items, err := loader.ReadFragment(ask)
	require.NoError(t, err)
	require.Len(t, items, 1, "a canonical URI addresses exactly one bundle, so exactly one item answers")
	return mustParseProducerRef(t, items[0].TrustRef)
}

// projectTrustRef is the trust.Ref the PROJECT copy gates under.
func projectTrustRef(t *testing.T, loader *bundles.Loader) trust.Ref {
	t.Helper()
	ref := trustRefOf(t, loader, projectFragmentRef)
	require.True(t, ref.IsLocal, "a project bundle's item must gate as project-local")
	return ref
}

// companionTrustRef is the same for the COMPANION copy, reached through the
// loader route under its own canonical URI.
func companionTrustRef(t *testing.T, loader *bundles.Loader) trust.Ref {
	t.Helper()
	ref := trustRefOf(t, loader, companionFragmentRef)
	require.True(t, ref.IsCompanion, "the companion copy's ref must gate as companion")
	return ref
}

// TestSameNamedBundles_RefRejectDoesNotLeakBetweenSources is the human's HARD
// CONSTRAINT on two bundles sharing a declared name, direction 1.
//
// A countersignature is keyed by CountersignRef, which derives the address
// from BundleRef.Identity(). The source class is carried in the identity's
// scheme ("ctxloom+companion:" vs "ctxloom+local:"), and that scheme is what
// separates two same-named items: CountersignRef must keep CanonicalURL, not
// collapse to Key() alone, or both sub-cases below fail.
//
// The source class is in the trust ref AND the resolution key, so both copies
// are reachable through the loader and the two directions below are symmetric
// by construction.
//
// So: reject the item in ONE of the two same-named bundles and the
// differently-contented item of the same name in the OTHER must still be
// delivered. Proved in BOTH directions, because a key collapse would be
// symmetric and testing one direction cannot see it.
//
// REF-scoped rejections only. A content-scoped reject is deliberately
// ref-agnostic (it follows identical bytes wherever they appear) and would
// withhold both copies whatever the refs were — which is why the two bodies
// here are deliberately DIFFERENT, and why the converse direction gets its own
// test below.
func TestSameNamedBundles_RefRejectDoesNotLeakBetweenSources(t *testing.T) {
	const projectBody = "PROJECT-DISTINCT-BODY-a41f"

	t.Run("rejecting the companion copy leaves the project copy deliverable", func(t *testing.T) {
		cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		fx := newTrustFixture(t)
		gate := &contentGate{cfg: cfg, records: fx.records()}
		loader := sameNameLoader(t, projectBody)
		pipe := bundles.NewPipeline(loader, gate, bundles.LinksUnchecked(), true)

		// Sanity: BOTH copies deliver first, through the SAME route. Without
		// this the assertions below pass on an item that was never reachable.
		got, err := pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content, "the project URI must reach the PROJECT copy")
		companionBefore, err := pipe.GetFragment(companionFragmentRef)
		require.NoError(t, err, "the companion URI must reach the COMPANION copy before any rejection")
		require.NotEqual(t, projectBody, companionBefore.Content,
			"the two URIs must reach DIFFERENT bytes, or a leak between them is invisible")

		fx.rejectRef(companionTrustRef(t, loader))

		_, err = pipe.GetFragment(companionFragmentRef)
		assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
			"the rejected companion item must be withheld, got %v", err)

		got, err = pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err,
			"rejecting the COMPANION's isolation-axes must not withhold the PROJECT's differently-contented one; "+
				"once both bundles declare the same name, only the trust ref's source class separates them")
		assert.Equal(t, projectBody, got.Content)
	})

	t.Run("rejecting the project copy leaves the companion copy deliverable", func(t *testing.T) {
		cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		fx := newTrustFixture(t)
		gate := &contentGate{cfg: cfg, records: fx.records()}
		loader := sameNameLoader(t, projectBody)
		pipe := bundles.NewPipeline(loader, gate, bundles.LinksUnchecked(), true)

		got, err := pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content)
		companionBefore, err := pipe.GetFragment(companionFragmentRef)
		require.NoError(t, err)

		fx.rejectRef(projectTrustRef(t, loader))

		_, err = pipe.GetFragment(projectFragmentRef)
		assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
			"the rejected project item must be withheld, got %v", err)

		gotCompanion, err := pipe.GetFragment(companionFragmentRef)
		require.NoError(t, err,
			"rejecting the PROJECT's isolation-axes must not withhold the COMPANION's; a user who rejects "+
				"their own copy has said nothing about the one a companion ships")
		assert.Equal(t, companionBefore.Content, gotCompanion.Content)
	})
}

// TestSameNamedBundles_ContentRejectStillFollowsIdenticalBytes is direction 2,
// and it pulls against direction 1 on purpose.
//
// A CONTENT rejection omits the ref entirely (ContentRejectCountersignPayload,
// spec §5.3) so that rejected bytes stay rejected after a rename, a move, or a
// copy into another bundle. Separating the two same-named items by trust ref
// must not buy that back: when the two copies carry the SAME bytes, one
// content rejection must withhold BOTH — via two independent delivery routes
// that share no gate call.
//
// Direction 1 proves ref rejections do not leak; this proves the fix did not
// achieve that by making rejections ref-scoped in general.
func TestSameNamedBundles_ContentRejectStillFollowsIdenticalBytes(t *testing.T) {
	body := companionSharedFragmentBody

	cfg := gatedFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	gate := &contentGate{cfg: cfg, records: fx.records()}
	// The project copy ships the companion's EXACT bytes — the vendored-copy
	// case, and the one where ref separation could hide a rejection.
	loader := sameNameLoader(t, body)
	pipe := bundles.NewPipeline(loader, gate, bundles.LinksUnchecked(), true)

	got, err := pipe.GetFragment(projectFragmentRef)
	require.NoError(t, err)
	require.Equal(t, body, got.Content, "the fixture must genuinely duplicate the companion's bytes")

	// No ref anywhere in this write: bytes only.
	fx.rejectContent(trust.KindFragment, signing.FormRaw, fragmentBytes(body))

	_, err = pipe.GetFragment(projectFragmentRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a content rejection must follow the bytes into the project bundle, got %v", err)

	_, err = pipe.GetFragment(companionFragmentRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"the same content rejection must withhold the companion copy too — separating the two by trust ref "+
			"must not make rejections ref-scoped")
}
