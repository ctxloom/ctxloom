package mcpschema

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U026-F03 is REFUTED, and this is the pin that keeps it refuted. The register
// says agent_recv's advertised wait prose hard-codes "default 60, max 600"
// while the real numbers live as Go constants elsewhere, ungated. It does not:
// RecvWaitDoc is derived from RecvWaitDefault and RecvWaitMax in the same
// declaration block ClampRecvWait enforces. Re-hardcoding the prose sends this
// red — which is the point, because a model reads the advertised maximum and
// plans around it, so a clamp disagreeing with the text cuts a caller off at a
// boundary it was told it could ask for.
func TestRecvWaitDoc_QuotesTheBoundsItEnforces(t *testing.T) {
	assert.Contains(t, RecvWaitDoc, strconv.Itoa(int(RecvWaitDefault.Seconds())),
		"the advertised default must be the constant ClampRecvWait applies")
	assert.Contains(t, RecvWaitDoc, strconv.Itoa(int(RecvWaitMax.Seconds())),
		"the advertised maximum must be the constant ClampRecvWait clamps to")
}

// The enforcement half: the numbers in the prose are the boundaries the clamp
// actually honours.
func TestClampRecvWait_HonoursTheAdvertisedBounds(t *testing.T) {
	for _, absent := range []int{0, -1, -600} {
		assert.Equal(t, RecvWaitDefault, ClampRecvWait(absent),
			"an absent/zero/negative wait takes the advertised default")
	}
	max := int(RecvWaitMax.Seconds())
	assert.Equal(t, RecvWaitMax, ClampRecvWait(max),
		"the advertised maximum is grantable, not one second past the limit")
	assert.Equal(t, RecvWaitMax, ClampRecvWait(max+1), "anything past it clamps")
	assert.Equal(t, 5*time.Second, ClampRecvWait(5), "an in-range wait is honoured verbatim")
}

// The generated surface is what the model actually reads, so the derivation
// has to survive projection: agent_recv's golden must advertise exactly
// RecvWaitDoc, not a second copy of the same sentence.
func TestAgentRecvGolden_AdvertisesTheDerivedWaitDoc(t *testing.T) {
	spec, ok := ToolByName(ToolAgentRecv)
	require.True(t, ok, "agent_recv has a generated golden")
	var in struct {
		Properties struct {
			Wait struct {
				Description string `json:"description"`
			} `json:"wait"`
		} `json:"properties"`
	}
	require.NoError(t, json.Unmarshal(spec.InputSchema, &in))
	assert.Equal(t, RecvWaitDoc, in.Properties.Wait.Description,
		"the advertised wait doc is the derived constant, not a hand-copied sentence")
}
