package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestStructuredArms_RejectUnknownFormatAlike pins --format handling across
// BOTH structured render arms. The global --format flag accepts five values,
// but the streaming commands support only text and json and reject the rest
// themselves (see format.go). Which arm a structured run takes is decided by
// isolation policy, not by the user -- so a value the user typed must mean the
// same thing on both, or `--format yaml` is an error on a host run and
// silently degrades to raw prose in a container.
func TestStructuredArms_RejectUnknownFormatAlike(t *testing.T) {
	const bogus = "yaml"

	t.Run("go-plugin arm", func(t *testing.T) {
		events := make(chan agent.ChatEvent)
		close(events)
		var out strings.Builder
		err := renderChatEvents(&out, bogus, events)
		require.Error(t, err, "the go-plugin arm rejects a format it cannot render")
		assert.Contains(t, err.Error(), bogus)
	})

	t.Run("container owned-run arm", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		ch := make(chan *agentcoordpb.AgentEvent, 4)
		ch <- ownedFinalStart("m-1")
		ch <- ownedDelta("m-1", "prose the user did not ask to see")
		ch <- ownedTurnIdle()

		var out strings.Builder
		_, err := renderOwnedRunEvents(ctx, &out, bogus, ownedTestRunID, ch, make(chan string, 4), true)

		require.Error(t, err, "the container arm must reject the same format the go-plugin arm rejects")
		require.NotErrorIs(t, err, context.DeadlineExceeded, "rejected, not parked")
		assert.Contains(t, err.Error(), bogus)
		assert.Empty(t, out.String(),
			"an unsupported format must render nothing, not fall back to raw text the caller never asked for")
	})
}
