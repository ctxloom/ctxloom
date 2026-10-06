package profiles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

const (
	ownRepoURL   = "https://github.com/acme/tools"
	otherRepoURL = "https://github.com/evil/elsewhere"
)

// A remote profile — SourceURL set — may name only content of the
// repository it was shipped in, through every field that carries a bundle
// reference. A local profile (SourceURL "") may name any repository.
func TestCheckOwnRepo(t *testing.T) {
	own := ownRepoURL + "@bundles/kit"
	other := otherRepoURL + "@bundles/payload"
	cases := []struct {
		name    string
		profile Profile
		bad     string
	}{
		{"own bundles, own parent, own items", Profile{
			Bundles:     []string{own, "git@github.com:acme/tools@bundles/other"},
			Parents:     []string{own + "#profiles/base"},
			BundleItems: []string{own + "#fragments/f"},
			Commands:    []string{own + "#commands/c"},
			Skills:      []string{own + "#skills/s"},
			Fragments:   []FragmentRef{{Name: own + "#fragments/g"}},
		}, ""},
		{"bundle from another repo", Profile{Bundles: []string{own, other}}, other},
		{"parent from another repo", Profile{Parents: []string{other + "#profiles/base"}}, other + "#profiles/base"},
		{"bundle item from another repo", Profile{BundleItems: []string{other + "#fragments/f"}}, other + "#fragments/f"},
		{"command from another repo", Profile{Commands: []string{other + "#commands/c"}}, other + "#commands/c"},
		{"skill from another repo", Profile{Skills: []string{other + "#skills/s"}}, other + "#skills/s"},
		{"fragment from another repo", Profile{Fragments: []FragmentRef{{Name: other + "#fragments/g"}}}, other + "#fragments/g"},
		{"the consumer's local content", Profile{Bundles: []string{"ctxloom:local@bundles/kit"}}, "ctxloom:local@bundles/kit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := tc.profile
			p.Name = own + "#profiles/dev"
			p.SourceURL = ownRepoURL
			err := p.CheckOwnRepo()
			if tc.bad == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrCrossRepoReference)
			assert.Contains(t, err.Error(), p.Name, "the refusal names the profile")
			assert.Contains(t, err.Error(), tc.bad, "the refusal names the reference that broke the rule")

			local := tc.profile
			assert.NoError(t, local.CheckOwnRepo(), "a local profile may name any repository")
		})
	}
}

// A seeded remote profile that reaches into another repository fails to
// load. A profile inheriting from it is resolved without that parent, under a
// fail-loudly finding naming the refusal — the policy every unloadable parent
// already gets.
func TestLoad_RemoteProfileReachingIntoAnotherRepoFails(t *testing.T) {
	key, ok := remote.CanonicalProfileKey(ownRepoURL + "@bundles/kit#profiles/dev")
	require.True(t, ok)
	bad := otherRepoURL + "@bundles/payload"
	seed := map[string]*Profile{
		key: {Name: key, Path: SeededProfilePathPrefix + key, SourceURL: ownRepoURL, Bundles: []string{bad}},
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	_, err := loader.Load(key)
	require.ErrorIs(t, err, ErrCrossRepoReference)
	assert.Contains(t, err.Error(), bad)

	child := map[string]*Profile{
		"ctxloom+local:project#profiles/mine": {Name: "ctxloom+local:project#profiles/mine", Parents: []string{key}},
	}
	var found report.Collector
	loader = NewLoader(nil, WithSeededProfiles(seed), WithSeededProfiles(child), WithReporter(&found))
	resolved, err := loader.ResolveProfile("mine", nil)
	require.NoError(t, err)
	assert.NotContains(t, resolved.Bundles, bad, "nothing of the violating parent is inherited")
	fatal := found.All().Fatal()
	require.Len(t, fatal, 1)
	assert.Contains(t, fatal[0].Text, ErrCrossRepoReference.Error())
	assert.Contains(t, fatal[0].Text, bad)
}

// A local profile drawing on bundles of two different repositories resolves.
func TestLoad_LocalProfileMixingRemotesResolves(t *testing.T) {
	key := "ctxloom+local:project#profiles/mix"
	seed := map[string]*Profile{
		key: {Name: key, Bundles: []string{ownRepoURL + "@bundles/kit", otherRepoURL + "@bundles/payload"}},
	}
	loader := NewLoader(nil, WithSeededProfiles(seed))

	resolved, err := loader.ResolveProfile("mix", nil)
	require.NoError(t, err)
	assert.Len(t, resolved.Bundles, 2)
}
