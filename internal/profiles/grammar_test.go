package profiles

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// grammarLoader returns a loader seeded with one remote bundle profile and one
// local bundle profile, with an alias resolver mapping "myrem" to defaultURL.
// This is the harness for the reference-grammar pins: which spellings reach
// which seed entries.
func grammarLoader(t *testing.T) *Loader {
	t.Helper()
	remoteKey := defaultURI + "//bundles/ai-developer#profiles/developer"
	localKey := "ctxloom+local:tools#profiles/probe"
	seed := map[string]*Profile{
		remoteKey: {
			Name:    remoteKey,
			Path:    SeededProfilePathPrefix + remoteKey,
			Bundles: []string{defaultURL + "@bundles/testing"},
		},
		localKey: {
			Name:    localKey,
			Path:    SeededProfilePathPrefix + localKey,
			Bundles: []string{"tools"},
		},
	}
	fs := afero.NewMemMapFs()
	require.NoError(t, afero.WriteFile(fs, "/profiles/personal/go-developer.yaml",
		[]byte("description: local subdir profile\n"), 0644))
	return NewLoader([]string{"/profiles"},
		WithFS(fs),
		WithSeededProfiles(seed),
		WithRemoteURLResolver(func(alias string) string {
			if alias == "myrem" {
				return defaultURL
			}
			return ""
		}))
}

// TestLoad_ProfileRefGrammar pins which spellings resolve. The alias form
// ("<alias>/<bundle>#profiles/<name>") must reach the same seed entry as the
// canonical URL; selector-less two-segment names stay local-only.
func TestLoad_ProfileRefGrammar(t *testing.T) {
	tests := []struct {
		name    string
		ref     string
		wantKey string
	}{
		{"canonical URL", defaultURL + "@bundles/ai-developer#profiles/developer",
			defaultURI + "//bundles/ai-developer#profiles/developer"},
		{"alias form", "myrem/ai-developer#profiles/developer",
			defaultURI + "//bundles/ai-developer#profiles/developer"},
		{"alias form with version pin", "myrem/ai-developer#profiles/developer@abc1234",
			defaultURI + "//bundles/ai-developer#profiles/developer"},
		{"local bundle short form", "tools#profiles/probe",
			"ctxloom+local:tools#profiles/probe"},
		{"explicit ctxloom:local form", "ctxloom:local@bundles/tools#profiles/probe",
			"ctxloom+local:tools#profiles/probe"},
		{"subdir local profile (selector-less two-segment name)", "personal/go-developer",
			"personal/go-developer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := grammarLoader(t)
			p, err := loader.Load(tt.ref)
			require.NoError(t, err)
			assert.Equal(t, tt.wantKey, p.Name)
		})
	}
}

// TestLoad_BundleProfileMiss_HasPullHint pins the unified not-found: any
// selector-carrying (or scheme-qualified) miss explains that bundle profiles
// resolve only via the lockfile seed and names the fix.
func TestLoad_BundleProfileMiss_HasPullHint(t *testing.T) {
	tests := []struct {
		name string
		ref  string
	}{
		{"unknown alias", "otherrem/ai-developer#profiles/developer"},
		{"unknown bundle", "nosuch#profiles/x"},
		{"scheme-qualified unpulled", "https://github.com/acme/repo@bundles/x#profiles/y"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loader := grammarLoader(t)
			_, err := loader.Load(tt.ref)
			require.Error(t, err)
			assert.ErrorIs(t, err, errs.ErrProfileNotFound)
			assert.Contains(t, err.Error(), "ctxloom deps pull")
		})
	}
}

// TestValidateProfileName_HashReserved pins the reserved character: '#' can
// never appear in a local profile name, so a "#profiles/" ref is structurally
// a bundle-profile ref.
func TestValidateProfileName_HashReserved(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"plain name", "developer", false},
		{"subdir name", "personal/go-developer", false},
		{"hash in name", "weird#name", true},
		{"selector-shaped name", "x#profiles/y", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateProfileName(tt.input)
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "reserved")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// TestResolveProfile_AliasParent pins the end-to-end case that motivated the
// alias-aware lookup: a directory profile whose parent is written in the
// "<alias>/<bundle>#profiles/<name>" form must merge the parent's content
// exactly like the canonical spelling.
func TestResolveProfile_AliasParent(t *testing.T) {
	loader := grammarLoader(t)
	fs := loader.fs
	require.NoError(t, afero.WriteFile(fs, "/profiles/dev.yaml",
		[]byte("parents:\n  - myrem/ai-developer#profiles/developer\nbundles:\n  - own-bundle\n"), 0644))

	resolved, err := loader.ResolveProfile("dev", nil)
	require.NoError(t, err)
	assert.Contains(t, resolved.Bundles, defaultURL+"@bundles/testing",
		"alias-spelled parent must merge the seeded bundle-profile content")
	assert.Contains(t, resolved.Bundles, "own-bundle")
}

// TestLoad_AliasWithoutResolver_FailsWithHint pins the degraded case: with no
// registry resolver the alias form cannot resolve, and the error still points
// at the pull-based fix rather than a bare not-found.
func TestLoad_AliasWithoutResolver_FailsWithHint(t *testing.T) {
	loader := NewLoader([]string{"/profiles"}, WithFS(afero.NewMemMapFs()))
	_, err := loader.Load("myrem/ai-developer#profiles/developer")
	require.Error(t, err)
	assert.ErrorIs(t, err, errs.ErrProfileNotFound)
	assert.Contains(t, err.Error(), "ctxloom deps pull")
}

// TestResolveProfile_VisitedKeyIsCanonical pins that the cycle-detection and
// memo key for the profile the caller NAMED is the same canonical identity every
// recursive step uses. ResolveProfile seeded visited with the caller's raw
// spelling while resolveProfileRecursive canonicalizes every parent, so the top
// frame was keyed differently from every frame below it: the same profile,
// reached again through its canonical spelling, was loaded and resolved a second
// time and only recognised one frame later.
func TestResolveProfile_VisitedKeyIsCanonical(t *testing.T) {
	const alias = "myrem/ai-developer#profiles/developer"
	canonical := defaultURI + "//bundles/ai-developer#profiles/developer"

	loader := grammarLoader(t)
	visited := map[string]bool{}
	resolved, err := loader.ResolveProfile(alias, visited)
	require.NoError(t, err)
	assert.Contains(t, resolved.Bundles, defaultURL+"@bundles/testing")

	assert.True(t, visited[canonical], "visited must be keyed by the canonical identity")
	assert.False(t, visited[alias], "the raw alias spelling must not be a second identity for the same profile")
}

// TestResolveProfile_SpellingsAgree pins that the alias and canonical spellings
// of one bundle profile resolve to the same content, whichever the caller names.
func TestResolveProfile_SpellingsAgree(t *testing.T) {
	canonical := defaultURL + "@bundles/ai-developer#profiles/developer"

	viaAlias, err := grammarLoader(t).ResolveProfile("myrem/ai-developer#profiles/developer", nil)
	require.NoError(t, err)
	viaCanonical, err := grammarLoader(t).ResolveProfile(canonical, nil)
	require.NoError(t, err)

	assert.Equal(t, viaCanonical.Bundles, viaAlias.Bundles)
	assert.Equal(t, viaCanonical.SourceRef, viaAlias.SourceRef)
}
