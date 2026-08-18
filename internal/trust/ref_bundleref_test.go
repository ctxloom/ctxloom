package trust

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

// TestRefAsBundleRef_InternalClasses pins that each of the three flag-carried
// classes mints the matching BundleRef class, with the item selector carried
// across when the Ref addresses one.
func TestRefAsBundleRef_InternalClasses(t *testing.T) {
	for _, tt := range []struct {
		name string
		ref  Ref
		want string
	}{
		{"builtin bundle", Ref{IsBuiltin: true, Bundle: "isolation"}, "ctxloom+builtin:isolation"},
		{"local item", Ref{IsLocal: true, Bundle: "lang/go", Kind: KindFragment, Name: "solid"},
			"ctxloom+local:lang/go#fragments/solid"},
		{"companion item", Ref{IsCompanion: true, Bundle: "taskloom", Kind: KindMCP, Name: "server"},
			"ctxloom+companion:taskloom#mcp/server"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ref.AsBundleRef()
			require.NoError(t, err)
			assert.Equal(t, tt.want, got.String())
		})
	}
}

// TestRefAsBundleRef_RemoteCleansBeforeMinting pins that AsBundleRef converts
// ordinary, previously-working RepoURL spellings — a git@ remote, a trailing
// ".git", an upper-case GitHub owner — rather than tripping BundleRef's
// stricter refusals for them (R2, and the .git/www. refusals). Without the
// CanonicalRepoURL pre-clean this conversion documents, countersignRef would
// have started failing to address ordinary remote content the moment it
// shipped.
func TestRefAsBundleRef_RemoteCleansBeforeMinting(t *testing.T) {
	for _, tt := range []struct {
		name string
		ref  Ref
	}{
		{"trailing .git", Ref{RepoURL: "https://github.com/acme/repo.git", Bundle: "toolkit", Kind: KindFragment, Name: "x"}},
		{"git@ scp form", Ref{RepoURL: "git@github.com:acme/repo.git", Bundle: "toolkit", Kind: KindFragment, Name: "x"}},
		{"upper-case owner on a known forge", Ref{RepoURL: "https://github.com/Acme/Repo", Bundle: "toolkit", Kind: KindFragment, Name: "x"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.ref.AsBundleRef()
			require.NoError(t, err)
			assert.Equal(t, "ctxloom+git://github.com/acme/repo//bundles/toolkit#fragments/x", got.Identity())
		})
	}
}

// TestRefAsBundleRef_FileRemote pins the ClassFile path: an absolute local
// repository path converts to ctxloom+file, no host.
func TestRefAsBundleRef_FileRemote(t *testing.T) {
	ref := Ref{RepoURL: "file:///srv/content", Bundle: "lang/go"}
	got, err := ref.AsBundleRef()
	require.NoError(t, err)
	assert.Equal(t, "ctxloom+file:///srv/content//bundles/lang/go", got.String())
}

// TestRefAsBundleRef_UnfoldableSpellingErrors pins that a spelling
// CanonicalRepoURL itself does not fold — a "www." host outside the known
// case-folding forges — still reaches ParseBundleRef unfolded and is refused
// there, so AsBundleRef reports an error rather than silently guessing an
// identity for it.
func TestRefAsBundleRef_UnfoldableSpellingErrors(t *testing.T) {
	ref := Ref{RepoURL: "https://www.git.example.com/acme/repo", Bundle: "toolkit"}
	_, err := ref.AsBundleRef()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRefSyntax)
}

// TestRefAsBundleRef_ZeroRefErrors pins that the zero Ref — produced only
// alongside an error every caller already checks (ParseItemRef's failure
// arms) — cannot convert: BundleRef's minters refuse an empty bundle name.
func TestRefAsBundleRef_ZeroRefErrors(t *testing.T) {
	_, err := Ref{}.AsBundleRef()
	require.Error(t, err)
}

// TestRefFromBundleRef_RoundTripsWithAsBundleRef pins RefFromBundleRef as
// AsBundleRef's mechanical inverse, one class at a time: converting a
// BundleRef to a Ref and back through AsBundleRef must reproduce the exact
// same canonical string. This is the guard for repoURLBranch — a mutation
// that broke the shared class-branch helper (e.g. swapped which class
// renders "file" and which renders "https") would show up here as a
// round-trip that changes the reference's class.
func TestRefFromBundleRef_RoundTripsWithAsBundleRef(t *testing.T) {
	git, err := GitRef("github.com", "/acme/repo", "toolkit")
	require.NoError(t, err)
	git, err = git.WithItem(KindFragment, "x")
	require.NoError(t, err)

	file, err := FileRef("/srv/content", "lang/go")
	require.NoError(t, err)

	builtin, err := BuiltinRef("isolation")
	require.NoError(t, err)

	local, err := LocalRef("lang/go")
	require.NoError(t, err)
	local, err = local.WithItem(KindFragment, "solid")
	require.NoError(t, err)

	companion, err := CompanionRef("taskloom")
	require.NoError(t, err)
	companion, err = companion.WithItem(KindMCP, "server")
	require.NoError(t, err)

	for _, tt := range []struct {
		name string
		br   BundleRef
	}{
		{"git item", git},
		{"file bundle", file},
		{"builtin bundle", builtin},
		{"local item", local},
		{"companion item", companion},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ref := RefFromBundleRef(tt.br)
			assert.Equal(t, tt.br.Bundle, ref.Bundle)
			assert.Equal(t, tt.br.Kind, ref.Kind)
			assert.Equal(t, tt.br.Item, ref.Name)

			got, err := ref.AsBundleRef()
			require.NoError(t, err)
			assert.Equal(t, tt.br.String(), got.String(),
				"RefFromBundleRef must be AsBundleRef's mechanical inverse")
		})
	}
}

// TestRefFromBundleRef_CompanionCarriesCanonicalURLToken pins the one field
// mapping that is NOT a bare copy for ClassCompanion: RepoURL must be stamped
// to remote.CompanionSource, because Ref.CanonicalURL has no IsCompanion
// branch of its own and falls through to CanonicalRepoURL(r.RepoURL), which
// only recognizes that exact token. Without this stamp a round-tripped
// companion Ref would key under a DIFFERENT CanonicalURL than one
// trust.ParseItemRef builds from the same source.
func TestRefFromBundleRef_CompanionCarriesCanonicalURLToken(t *testing.T) {
	br, err := CompanionRef("taskloom")
	require.NoError(t, err)
	ref := RefFromBundleRef(br)
	assert.Equal(t, remote.CompanionSource, ref.RepoURL)
	assert.Equal(t, remote.CompanionSource, ref.CanonicalURL())
}

// TestRefDisplayRef_RendersGrammarA pins DisplayRef's normal path: it mints
// through AsBundleRef and renders String() (WITH any version), the same
// operation CountersignRef uses but rendering Identity's version-carrying
// sibling instead.
func TestRefDisplayRef_RendersGrammarA(t *testing.T) {
	ref := Ref{IsLocal: true, Bundle: "lang/go", Kind: KindFragment, Name: "solid"}
	assert.Equal(t, "ctxloom+local:lang/go#fragments/solid", ref.DisplayRef())
}

// TestRefDisplayRef_UnconvertibleFallsBackStably pins DisplayRef's fallback
// for a Ref AsBundleRef refuses (the zero Ref, or any other unconvertible
// spelling): the same deterministic, unmistakable "ctxloom+unaddressable:%#v"
// spelling operations.CountersignRef falls back to for the identical failure,
// so the two never disagree on how to spell "cannot address this Ref".
func TestRefDisplayRef_UnconvertibleFallsBackStably(t *testing.T) {
	ref := Ref{}
	assert.Equal(t, fmt.Sprintf("ctxloom+unaddressable:%#v", ref), ref.DisplayRef())
}
