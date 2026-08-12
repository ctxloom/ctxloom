package coord

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// When sendTerminal's bounded evict-retry is exhausted the run's one terminal
// event is dropped — which evades seq-gap detection (nothing follows to reveal
// the hole) and hangs any watcher on that run (adaptConsumerFeed ends its feed
// only on RunCompleted). The drop used to be entirely silent, so a hung viewer
// was undiagnosable from the coordinator's logs. The drop must now emit a
// diagnostic that NAMES the run whose terminal was lost.
//
// Exhaustion is forced with a ring saturated by ANOTHER run's terminal, which
// evictOneNonTerminal (F09) correctly refuses to sacrifice: no room is ever
// freed, so every attempt fails and the bound is reached.
func TestSendTerminal_DropAfterExhaustion_LogsTheDroppedRun(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	ch := make(chan *agentcoordpb.AgentEvent, 1)
	ch <- terminalEvent(1, "run-other") // a terminal that must not be evicted → no slot ever frees

	sendTerminal(ch, terminalEvent(2, "run-dropped"))

	out := buf.String()
	require.NotEmpty(t, out,
		"an exhausted terminal drop must not be silent — a hung watcher would otherwise be undiagnosable")
	assert.Contains(t, out, "run-dropped",
		"the diagnostic must NAME the run whose terminal event was dropped")
	assert.Contains(t, out, "dropped terminal",
		"the diagnostic must say a terminal event was dropped")

	// The pre-existing terminal was preserved, not consumed (F09 holds here too).
	require.Len(t, ch, 1)
	assert.Equal(t, "run-other", (<-ch).GetRunId())
}
