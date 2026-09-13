package engine

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Distribution's zero value is UNSET, and Validate refuses it: an engine that
// declared nothing must not default-ship. If shipping were the zero value, a
// forgotten field would silently put a double into the default image set —
// the forgotten-entry failure the descriptor exists to close.
func TestValidate_RefusesUnsetDistribution(t *testing.T) {
	d := validDescriptor()
	d.Distribution = agent.DistributionUnset
	err := d.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Distribution")
}

// Every non-zero member is a decision Validate accepts.
func TestValidate_AcceptsEveryDecidedDistribution(t *testing.T) {
	for _, dist := range []agent.Distribution{agent.DistributionDefault, agent.DistributionOptIn, agent.DistributionTestOnly} {
		d := validDescriptor()
		d.Distribution = dist
		assert.NoError(t, d.Validate(), "%v", dist)
	}
}

// A value outside the enum is as undecided as the zero value.
func TestValidate_RefusesAnUnknownDistribution(t *testing.T) {
	d := validDescriptor()
	d.Distribution = agent.Distribution(99)
	assert.Error(t, d.Validate())
}
