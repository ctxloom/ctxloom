package profiles

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/errs"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

func profileMissFix(t *testing.T, err error) string {
	t.Helper()
	require.ErrorIs(t, err, errs.ErrProfileNotFound)
	var r report.Remediable
	require.True(t, errors.As(err, &r), "a profile miss names its fix: %v", err)
	return r.Remedy()
}

// TestLoad_BareNameMiss_SuggestsTheOneInstalledProfileOfThatName: a bare
// name means the project's own profile, but a new user types the name they
// saw in a listing (`-p developer`) for a profile another bundle ships. When
// exactly one installed profile carries that name, the miss names its ref.
func TestLoad_BareNameMiss_SuggestsTheOneInstalledProfileOfThatName(t *testing.T) {
	key := defaultURI + "//bundles/kit#profiles/developer"
	loader := NewLoader(nil, WithSeededProfiles(map[string]*Profile{key: {Name: key}}))

	for _, ask := range []string{"developer", "ctxloom+local:project#profiles/developer"} {
		_, err := loader.Load(ask)
		assert.Equal(t, fmt.Sprintf(didYouMeanProfileFix, key), profileMissFix(t, err), "asked %q", ask)
	}
	_, err := loader.ResolveProfile("developer", nil)
	assert.Equal(t, fmt.Sprintf(didYouMeanProfileFix, key), profileMissFix(t, err), "the launch path resolves through Load")
}

// TestLoad_BareNameMiss_WithoutAUniqueMatch_PointsAtTheListing: no match, or
// several, cannot be guessed between, so the fix is the listing.
func TestLoad_BareNameMiss_WithoutAUniqueMatch_PointsAtTheListing(t *testing.T) {
	a := defaultURI + "//bundles/one#profiles/developer"
	b := defaultURI + "//bundles/two#profiles/developer"
	loader := NewLoader(nil, WithSeededProfiles(map[string]*Profile{a: {Name: a}, b: {Name: b}}))

	_, err := loader.Load("developer")
	assert.Equal(t, listProfilesFix, profileMissFix(t, err), "two candidates: no guess")
	_, err = loader.Load("nosuch")
	assert.Equal(t, listProfilesFix, profileMissFix(t, err), "no candidate: the listing")
}
