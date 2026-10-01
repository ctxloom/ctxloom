package interaction

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/mcpschema"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// TestCoordinationHandler_ResolvesEveryCoordinationTool pins the dispatch:
// every tool the routes table classifies as coordination has a handler,
// and a name outside it is a startup error, never a silent fallthrough.
func TestCoordinationHandler_ResolvesEveryCoordinationTool(t *testing.T) {
	var rep report.Reporter
	for name, route := range mcpschema.Routes() {
		if route != mcpschema.RouteCoordination {
			continue
		}
		t.Run(name, func(t *testing.T) {
			h, err := coordinationHandler(rep, nil, "harp", "/cwd", name, false)
			require.NoError(t, err)
			assert.NotNil(t, h)
		})
	}
	_, err := coordinationHandler(rep, nil, "harp", "/cwd", "no_such_tool", false)
	require.Error(t, err)
}
