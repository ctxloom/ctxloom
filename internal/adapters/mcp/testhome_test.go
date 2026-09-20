package mcp

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// testHome builds a Home against a dead loopback endpoint — registration
// needs the value, not a live coordinator.
func testHome(t *testing.T) *coord.Home {
	t.Helper()
	h, err := coord.NewHome(context.Background(), coord.HomeConfig{
		URL:     "http://127.0.0.1:1/mcp",
		Token:   "t",
		RunID:   "run-x",
		Harness: "mock",
		Version: "test",
		Harp:    "run-x-harp",
	})
	require.NoError(t, err)
	t.Cleanup(func() { h.Close(0, "") })
	return h
}
