package bundles

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/errs"
)

// TestParseBundle_LoadsProfiles confirms the new ungated, compound `profiles:`
// bundle item kind round-trips through ParseBundle into the shared profile
// shape (profiles.Profile via the BundleProfile alias), composing leaves
// (fragments/commands/mcp/hooks/llm/parents/variables).
func TestParseBundle_LoadsProfiles(t *testing.T) {
	data := []byte(`version: "1.0.0"
fragments:
  reliability:
    content: "be reliable"
profiles:
  reliability-swift:
    description: "Reliability lens, Swift"
    tags: [review, reliability, swift]
    llm: fast
    bundles:
      - ctxloom:local@bundles/code-review#fragments/reliability
    parents:
      - ctxloom:local@bundles/code-review#profiles/base
    variables:
      lang: swift
`)
	b, err := ParseBundle(data)
	require.NoError(t, err)

	assert.Equal(t, 1, b.ProfileCount())
	assert.Equal(t, []string{"reliability-swift"}, b.ProfileNames())

	p, ok := b.Profiles["reliability-swift"]
	require.True(t, ok)
	assert.Equal(t, "Reliability lens, Swift", p.Description)
	assert.Equal(t, "fast", p.LLM, "a profile carries an llm preference (the engine selection)")
	assert.Equal(t, []string{"review", "reliability", "swift"}, p.Tags)
	require.Equal(t, []string{"ctxloom:local@bundles/code-review#fragments/reliability"}, p.Bundles)
	require.Equal(t, []string{"ctxloom:local@bundles/code-review#profiles/base"}, p.Parents)
	assert.Equal(t, "swift", p.Variables["lang"])
}

// TestParseBundle_NoProfiles_InitsEmptyMap pins that a bundle with no profiles
// still gets an initialized (non-nil) map, matching fragments/commands/mcp.
func TestParseBundle_NoProfiles_InitsEmptyMap(t *testing.T) {
	b, err := ParseBundle([]byte(`version: "1.0.0"`))
	require.NoError(t, err)
	assert.NotNil(t, b.Profiles)
	assert.Equal(t, 0, b.ProfileCount())
	assert.Empty(t, b.ProfileNames())
}

// TestBundleProfile_GateExempt is the explicit gate-exemption invariant at the
// content choke: a bundle's fragment STILL gates (a deny gate withholds it),
// but the profile DEFINITION is never run through the content trust gate — the
// bundle loader has no profile-resolution path, so the gate is never consulted
// for any "#profiles/" ref. (Profile resolution happens in the shared profile
// loader via the seed; the profile def is orchestration/config, ungated.)
func TestBundleProfile_GateExempt(t *testing.T) {
	b, err := ParseBundle([]byte(`version: "1.0.0"
fragments:
  f1:
    content: "FRAG-ONE"
profiles:
  p1:
    description: "p one"
    bundles:
      - ctxloom:local@bundles/kit#fragments/f1
`))
	require.NoError(t, err)
	b.Name = "kit"

	var gated []string
	denyGate := authorizerFunc(func(e Exposure) Verdict {
		gated = append(gated, exposureRefKey(e))
		return denyVerdict() // withhold everything the choke is consulted about
	})
	pipe := gatedPipe(NewLoader(seedLocal(map[string]*Bundle{"kit": b})), denyGate, false)

	// The constituent fragment gates at content assembly: a deny gate withholds it.
	_, err = pipe.GetFragment("kit#fragments/f1")
	require.ErrorIs(t, err, errs.ErrFragmentWithheld)

	// The gate was consulted for the fragment, but NEVER for the profile
	// definition — profiles are not a gated content kind.
	require.Contains(t, gated, "kit#fragments/f1")
	for _, ref := range gated {
		assert.NotContains(t, ref, ProfileSelectorForTest,
			"the content trust gate must never be consulted for a profile definition")
	}
}

// ProfileSelectorForTest mirrors remote.ProfileSelector without importing remote
// into this test (the bundles package already depends on remote, but the test
// only needs the literal to assert no profile ref reached the gate).
const ProfileSelectorForTest = "#profiles/"
