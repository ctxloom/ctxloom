package trust

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
