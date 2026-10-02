package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/composite/compositetest"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// An item the waiver admitted is DELIVERED, and nobody reviewed it: the
// stamp says the first (trusted, its own source) and the review state says
// the second. "accepted" would claim a human looked.
func TestResultOf_TheWaiverIsItsOwnSourceAndReadsAsPending(t *testing.T) {
	res := resultOf(bundles.Verdict{Allow: true, Reason: bundles.ReasonSigCheckDisabled})

	assert.True(t, res.Trusted())
	assert.Equal(t, trust.SourceSigCheckDisabled, res.Source)
	assert.Equal(t, trust.StatePending, res.State())
}

// A review-path gate (one built over records an operation just wrote) decides
// with the same posture as the generation it was built for.
func TestTrustOverRecords_CarriesTheGenerationsSignatureCheckPosture(t *testing.T) {
	root, records, retraction := compositetest.Ports()
	waived, err := composite.NewTrust(root, records, retraction, composite.WithoutSignatureCheck())
	require.NoError(t, err)
	for _, tc := range []struct {
		name string
		gen  composite.Trust
		want bool
	}{
		{"enforced", compositetest.Trust(), false},
		{"waived", waived, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.NewFixture(config.Fixture{})
			cfg.BindTrustForTesting(tc.gen)
			_, recs, _ := compositetest.Ports()
			assert.Equal(t, tc.want, trustOverRecords(cfg, recs, afero.NewMemMapFs()).SignatureCheckDisabled())
		})
	}
}
