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

// The name a project bundle and the one embedded builtin both answer to, the
// fragment they both ship, and the canonical URI each is addressed by. The
// bare name is deliberately absent: it names two bundles here and resolves to
// neither.
const (
	sharedBundleName   = "isolation"
	sharedFragmentName = "isolation-axes"
	projectFragmentRef = "ctxloom+local:" + sharedBundleName + "#fragments/" + sharedFragmentName
	builtinFragmentRef = "ctxloom+builtin:" + sharedBundleName + "#fragments/" + sharedFragmentName
)

// sameNameLoader composes the two readers production composes — project first,
// builtin second — over a project bundle called "isolation" whose
// "isolation-axes" fragment carries projectBody.
//
// Neither displaces the other. They are keyed by WHERE each was read, so both
// are in the resolved set and both are reachable through the LOADER route,
// each under its own canonical URI. That is the whole point of this file — two
// items of one declared name, from two sources, live in one session at once.
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

// builtinTrustRef is the same for the BUILTIN copy, reached through the loader
// route under its own canonical URI.
func builtinTrustRef(t *testing.T, loader *bundles.Loader) trust.Ref {
	t.Helper()
	ref := trustRefOf(t, loader, builtinFragmentRef)
	require.True(t, ref.IsBuiltin, "the builtin copy's ref must gate as builtin")
	return ref
}

// TestSameNamedBundles_RefRejectDoesNotLeakBetweenSources is the human's HARD
// CONSTRAINT on two bundles sharing a declared name, direction 1.
//
// A countersignature is keyed by CountersignRef, which derives the address
// from BundleRef.Identity(). The source class is carried in the identity's
// scheme ("ctxloom+builtin:" vs "ctxloom+local:") and, historically, also
// through Key's Bundle component — CanonicalURL as
// "ctxloom:builtin" vs "ctxloom:local", Key through its Bundle component
// ("builtin:isolation" vs "isolation") — and MEASURED, only the FIRST is
// load-bearing: normalising the "builtin:" prefix out of Key() leaves this
// test green, while dropping CanonicalURL from CountersignRef fails both
// sub-cases below. So the separation survives on Ref.IsBuiltin, set from the
// qualified TRUST ref, and Key()'s prefix is redundant rather than the
// guarantee. Do not "simplify" CountersignRef to Key() alone.
//
// What all of that rests on is that the source class is in the trust ref. It
// is now also the RESOLUTION key, so both copies are reachable through the
// loader and the two directions below are symmetric by construction rather
// than by one of them borrowing the unconditional injection route.
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

		// Sanity: BOTH copies deliver first, through the SAME route. Without
		// this the assertions below pass on an item that was never reachable.
		got, err := pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content, "the project URI must reach the PROJECT copy")
		builtinBefore, err := pipe.GetFragment(builtinFragmentRef)
		require.NoError(t, err, "the builtin URI must reach the BUILTIN copy before any rejection")
		require.NotEqual(t, projectBody, builtinBefore.Content,
			"the two URIs must reach DIFFERENT bytes, or a leak between them is invisible")
		require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
			"the injection route must deliver the builtin copy before any rejection")

		fx.rejectRef(builtinTrustRef(t, loader))

		_, err = pipe.GetFragment(builtinFragmentRef)
		assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
			"the rejected builtin item must be withheld, got %v", err)
		assert.False(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
			"the rejected builtin item must be withheld")

		got, err = pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err,
			"rejecting the BUILTIN's isolation-axes must not withhold the PROJECT's differently-contented one; "+
				"once both bundles declare the same name, only the trust ref's source class separates them")
		assert.Equal(t, projectBody, got.Content)
	})

	t.Run("rejecting the project copy leaves the builtin deliverable", func(t *testing.T) {
		cfg := config.NewFixture(config.Fixture{AppPaths: []string{testBaseDir}})
		fx := newTrustFixture(t)
		gate := &contentGate{cfg: cfg, records: fx.records()}
		loader := sameNameLoader(t, projectBody)
		pipe := bundles.NewPipeline(loader, gate, true)

		got, err := pipe.GetFragment(projectFragmentRef)
		require.NoError(t, err)
		require.Equal(t, projectBody, got.Content)
		builtinBefore, err := pipe.GetFragment(builtinFragmentRef)
		require.NoError(t, err)
		require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef))

		fx.rejectRef(projectTrustRef(t, loader))

		_, err = pipe.GetFragment(projectFragmentRef)
		assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
			"the rejected project item must be withheld, got %v", err)

		gotBuiltin, err := pipe.GetFragment(builtinFragmentRef)
		require.NoError(t, err,
			"rejecting the PROJECT's isolation-axes must not withhold the BUILTIN's; a user who rejects "+
				"their own copy has said nothing about the one shipped in the binary")
		assert.Equal(t, builtinBefore.Content, gotBuiltin.Content)

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

	got, err := pipe.GetFragment(projectFragmentRef)
	require.NoError(t, err)
	require.Equal(t, body, got.Content, "the fixture must genuinely duplicate the builtin's bytes")
	require.True(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef))

	// No ref anywhere in this write: bytes only.
	fx.rejectContent(trust.KindFragment, signing.FormRaw, []byte(body))

	_, err = pipe.GetFragment(projectFragmentRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"a content rejection must follow the bytes into the project bundle, got %v", err)

	_, err = pipe.GetFragment(builtinFragmentRef)
	assert.True(t, errors.Is(err, errs.ErrFragmentWithheld),
		"the same content rejection must withhold the builtin copy too — separating the two by trust ref "+
			"must not make rejections ref-scoped")

	assert.False(t, containsFragmentRef(cfg.ResolveBuiltinBundleFragments(gate), builtinIsolationFragmentRef),
		"the same content rejection must withhold the builtin copy too — separating the two by trust ref "+
			"must not make rejections ref-scoped")
}
