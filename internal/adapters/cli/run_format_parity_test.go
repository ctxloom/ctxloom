package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
)

// TestOwnedRunRender_RejectsUnknownFormat pins --format handling on the
// owned-run render arm. The global --format flag accepts five values, but the
// streaming commands support only text and json and reject the rest
// themselves (see format.go): `--format yaml` is an error, never a silent
// degrade to raw prose.
func TestOwnedRunRender_RejectsUnknownFormat(t *testing.T) {
	const bogus = "yaml"

	t.Run("owned-run arm", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		ch := make(chan *agentcoordpb.AgentEvent, 4)
		ch <- ownedFinalStart("m-1")
		ch <- ownedDelta("m-1", "prose the user did not ask to see")
		ch <- ownedTurnIdle()

		var out strings.Builder
		_, err := renderOwnedRunEvents(ctx, &out, bogus, ownedTestRunID, ch, make(chan string, 4), true)

		require.Error(t, err, "an unsupported format is refused")
		require.NotErrorIs(t, err, context.DeadlineExceeded, "rejected, not parked")
		assert.Contains(t, err.Error(), bogus)
		assert.Empty(t, out.String(),
			"an unsupported format must render nothing, not fall back to raw text the caller never asked for")
	})
}
