package delivery_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
)

// TestWriter_SessionHarp: the harp a session's writer tag names is what a
// sweep probes for liveness; the project's writer, and a session tag naming
// no harp, name none.
func TestWriter_SessionHarp(t *testing.T) {
	harp, ok := delivery.SessionWriter("brave-amber-fox").SessionHarp()
	require.True(t, ok)
	require.Equal(t, "brave-amber-fox", harp)

	_, ok = delivery.ProjectWriter.SessionHarp()
	require.False(t, ok, "the project writer is no session")

	_, ok = delivery.SessionWriter("").SessionHarp()
	require.False(t, ok, "a session tag naming no harp names no session to probe")
}
