package grpc

import (
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// agent.ExecutionMode crosses this wire by raw numeric cast in both directions
// — GRPCServer.Info casts Go->proto, RunTurn casts proto->Go — so the two enums
// are connascent by VALUE across two packages with nothing translating between
// them. Nothing but this test stands between a renumbered or extended enum on
// one side and a silent mode swap on the other: an INTERACTIVE launch would
// arrive as ONESHOT, i.e. a session that exits after the first response.
//
// The pairing is asserted by NAME so a value change on either side is caught,
// and the arity is asserted so ADDING a mode to one side without the other is
// caught too.
func TestExecutionMode_WireAndGoEnumsAgreeValueForValue(t *testing.T) {
	pairs := map[agent.ExecutionMode]ExecutionMode{
		agent.ModeInteractive: ExecutionMode_INTERACTIVE,
		agent.ModeOneshot:     ExecutionMode_ONESHOT,
	}

	require.Len(t, ExecutionMode_name, len(pairs),
		"the wire enum gained or lost a mode; agent.ExecutionMode and the casts in server.go must follow")

	for goMode, wireMode := range pairs {
		assert.Equal(t, int32(goMode), int32(wireMode),
			"agent.ExecutionMode(%d) and wire %s must share a numeric value; the wire is crossed by raw cast",
			int32(goMode), ExecutionMode_name[int32(wireMode)])
		// Both casts, as the production code performs them.
		assert.Equal(t, wireMode, ExecutionMode(goMode))
		assert.Equal(t, goMode, agent.ExecutionMode(wireMode))
	}

	// Every mode the Go side reports as supported must have a wire counterpart:
	// GRPCServer.Info casts each of these straight onto the wire.
	for _, m := range (&agent.BaseBackend{}).SupportedModes() {
		_, ok := ExecutionMode_name[int32(m)]
		assert.True(t, ok, "agent.ExecutionMode(%d) has no wire counterpart", int32(m))
	}
}
