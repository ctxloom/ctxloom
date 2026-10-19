package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// TestHostApp_AnUnknownToolIsRefused: the set the app serves is derived —
// every tool that reads the sessions root or the task store's history — and a
// name outside it is refused by name, never answered by a default.
func TestHostApp_AnUnknownToolIsRefused(t *testing.T) {
	_, err := NewHostApp(&config.Config{}, testLaunchFacts()).Serve(context.Background(), coord.Identity{Harp: "h"}, coord.HostRequest{Tool: "agent_run"})
	require.ErrorIs(t, err, coord.ErrUnknownHostTool)
}
