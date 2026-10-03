package coordgrpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/coord"
)

// A held run's roster entry carries its hold on the wire — kind, source and
// deadline in Unix seconds, 0 for a hold only clearing ends — and a run
// nothing holds carries none.
func TestRunsSnapshotToWire_CarriesTheHold(t *testing.T) {
	until := time.Date(2026, 10, 1, 17, 30, 0, 0, time.UTC)
	got := RunsSnapshotToWire(coord.RunsSnapshot{Runs: []coord.RunInfo{
		{RunID: "held", Hold: &coord.RunHold{Kind: "rate_limited", Source: "mock env=CLAUDE_CODE_OAUTH_TOKEN store=", Until: until}},
		{RunID: "cleared-only", Hold: &coord.RunHold{Kind: "rate_limited", Source: "run:x"}},
		{RunID: "free"},
	}}).GetRuns()
	require.Len(t, got, 3)
	assert.Equal(t, "rate_limited", got[0].GetHold().GetKind())
	assert.Equal(t, "mock env=CLAUDE_CODE_OAUTH_TOKEN store=", got[0].GetHold().GetSource())
	assert.Equal(t, until.Unix(), got[0].GetHold().GetUntilUnix())
	require.NotNil(t, got[1].GetHold())
	assert.Zero(t, got[1].GetHold().GetUntilUnix(), "no deadline is 0, never the zero time's seconds")
	assert.Nil(t, got[2].GetHold())
}
