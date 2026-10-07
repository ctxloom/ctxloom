package operations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/ident"
)

// TestCarriedSources_NameEveryCompanionWhoseProbeFailed: only a FAILED
// probe leaves a companion's content unknown; a companion that answered it
// has none, one never consented to and one absent contribute nothing to
// keep. Each is named by the stamp its servers carry (bundles.BundleSCM).
func TestCarriedSources_NameEveryCompanionWhoseProbeFailed(t *testing.T) {
	ref := func(bin string) ident.BundleRef {
		r, err := ident.CompanionRef(bin)
		require.NoError(t, err)
		return r
	}
	got := carriedSources([]bundles.Candidate{
		{Ref: ref("taskloom").BundleIdentity(), Reason: bundles.CandidateProbeFailed},
		{Ref: ref("ltk").BundleIdentity(), Reason: bundles.CandidateNoLoadout},
		{Ref: ref("gone").BundleIdentity(), Reason: bundles.CandidateAbsent},
	})
	require.Equal(t, []string{bundles.BundleSCM(ref("taskloom"))}, got)
}
