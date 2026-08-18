package operations

import (
	"errors"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// The name a project bundle and the one embedded builtin now both answer to,
// and the fragment they both ship.
const (
	sharedBundleName   = "isolation"
	sharedFragmentName = "isolation-axes"
	sharedFragmentRef  = sharedBundleName + "#fragments/" + sharedFragmentName
)

// sameNameLoader composes the two readers production composes — project first,
// builtin second — over a project bundle called "isolation" whose
// "isolation-axes" fragment carries projectBody.
//
// Both bundles now key to the BARE name "isolation" (slice I7 took the source
// class out of the resolution ref), so the builtin is shadowed as a bundle
// HANDLE. It still reaches the session, though: ResolveBuiltinBundleFragments
// injects builtins unconditionally, straight from the embedded reader, without
// consulting the catalog. That is the whole point of this file — two items of
// one name, from two sources, live in one session at once.
func sameNameLoader(t *testing.T, projectBody string) *bundles.Loader {
	t.Helper()
	data, err := yaml.Marshal(&bundles.Bundle{
		Name: sharedBundleName,
		Fragments: map[string]bundles.BundleFragment{
			sharedFragmentName: {Content: projectBody},
		},
	})
	require.NoError(t, err)
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/bundles/"+sharedBundleName+".yaml", data, 0o644))
	return bundles.NewLoader(
		bundles.NewProjectReader(fs, []string{"/bundles"}),
		bundles.NewBuiltinReader(),
	)
}

// builtinSharedFragmentBody is the builtin's own bytes for the shared
// fragment, read through the builtin READER rather than re-parsed from the
// embedded YAML — so a content rejection written over it is written over
// exactly what the injection route will hand the gate, not over a
// lookalike that survived a different parse.
func builtinSharedFragmentBody(t *testing.T) string {
	t.Helper()
	b, err := bundles.NewLoader(bundles.NewBuiltinReader()).Load(sharedBundleName)
	require.NoError(t, err)
	frag, ok := b.Fragments[sharedFragmentName]
	require.True(t, ok, "the embedded isolation bundle must ship %s", sharedFragmentName)
	require.NotEmpty(t, frag.Content)
	return frag.Content
}

// projectTrustRef is the trust.Ref the PROJECT copy gates under, taken from
// the item the loader actually produces rather than spelled by hand — a
// hand-spelled ref would keep passing if the loader started minting a
// different one, which is the failure this whole file is about.
func projectTrustRef(t *testing.T, loader *bundles.Loader) trust.Ref {
	t.Helper()
	items, err := loader.ReadFragment(sharedFragmentRef)
	require.NoError(t, err)
	require.Len(t, items, 1, "the bare name must resolve to the PROJECT copy; the builtin is shadowed")
	ref, _, _, err := trust.ParseItemRef(items[0].TrustRef)
	require.NoError(t, err)
	require.True(t, ref.IsLocal, "a project bundle's item must gate as project-local")
	return ref
}

// builtinTrustRef is the same for the BUILTIN copy, parsed from the ref the
// injection route constructs.
func builtinTrustRef(t *testing.T) trust.Ref {
	t.Helper()
	ref, _, _, err := trust.ParseItemRef(builtinIsolationFragmentRef)
	require.NoError(t, err)
	require.True(t, ref.IsBuiltin, "the injection route's ref must gate as builtin")
	return ref
}

// TestSameNamedBundles_RefRejectDoesNotLeakBetweenSources is the human's HARD
// CONSTRAINT on the shadowing rule, direction 1.
//
// A countersignature is keyed by CountersignRef = Ref.CanonicalURL() + "|" +
// Ref.Key(). Both halves carry the source class today — CanonicalURL as
// "ctxloom:builtin" vs "ctxloom:local", Key through its Bundle component
// ("builtin:isolation" vs "isolation") — and MEASURED, only the FIRST is
// load-bearing: normalising the "builtin:" prefix out of Key() leaves this
// test green, while dropping CanonicalURL from CountersignRef fails both
// sub-cases below. So the separation survives on Ref.IsBuiltin, set by
// ParseItemRef from the qualified TRUST ref, and Key()'s prefix is redundant
// rather than the guarantee. Do not "simplify" CountersignRef to Key() alone.
//
// What all of that rests on is that the source class stayed in the trust ref
// when slice I7 took it out of the RESOLUTION ref. Nothing else separates the
// two items: both bundles now answer to the bare name "isolation".
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

	t.Run("rejecting the builtin leaves the project copy deliverable", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		fx := newTrustFixture(t)
		gate := &contentGate{cfg: cfg, records: fx.records()}
		loader := sameNameLoader(t, projectBody)
		pipe := bundles.NewPipeline(loader, gate, true)

		// Sanity: BOTH routes deliver first. Without this the assertions below
		// pass on an item that was never reachable.
		got, err := pipe.GetFragment(sharedFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content, "the bare name must reach the PROJECT copy")
		require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
			"the injection route must deliver the builtin copy before any rejection")

		fx.rejectRef(builtinTrustRef(t))

		assert.False(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
			"the rejected builtin item must be withheld")

		got, err = pipe.GetFragment(sharedFragmentRef)
		require.NoError(t, err,
			"rejecting the BUILTIN's isolation-axes must not withhold the PROJECT's differently-contented one; "+
				"once both bundles key to the bare name, only the trust ref's source class separates them")
		assert.Equal(t, projectBody, got.Content)
	})

	t.Run("rejecting the project copy leaves the builtin deliverable", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		fx := newTrustFixture(t)
		gate := &contentGate{cfg: cfg, records: fx.records()}
		loader := sameNameLoader(t, projectBody)
		pipe := bundles.NewPipeline(loader, gate, true)

		got, err := pipe.GetFragment(sharedFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content)
		require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef))

		fx.rejectRef(projectTrustRef(t, loader))

		_, err = pipe.GetFragment(sharedFragmentRef)
		assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
			"the rejected project item must be withheld, got %v", err)

		assert.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
			"rejecting the PROJECT's isolation-axes must not withhold the BUILTIN's; a user who rejects "+
				"their own copy has said nothing about the one shipped in the binary")
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
	body := builtinSharedFragmentBody(t)

	cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
	fx := newTrustFixture(t)
	gate := &contentGate{cfg: cfg, records: fx.records()}
	// The project copy ships the builtin's EXACT bytes — the vendored-copy
	// case, and the one where ref separation could hide a rejection.
	loader := sameNameLoader(t, body)
	pipe := bundles.NewPipeline(loader, gate, true)

	got, err := pipe.GetFragment(sharedFragmentRef)
	require.NoError(t, err)
	require.Equal(t, body, got.Content, "the fixture must genuinely duplicate the builtin's bytes")
	require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef))

	// No ref anywhere in this write: bytes only.
	fx.rejectContent(trust.KindFragment, signing.FormRaw, []byte(body))

	_, err = pipe.GetFragment(sharedFragmentRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a content rejection must follow the bytes into the project bundle, got %v", err)

	assert.False(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
		"the same content rejection must withhold the builtin copy too — separating the two by trust ref "+
			"must not make rejections ref-scoped")
}
