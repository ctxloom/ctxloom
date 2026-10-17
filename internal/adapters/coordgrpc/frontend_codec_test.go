package coordgrpc

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// The roster fields the frontend port grew reach the wire: the parent harp a
// remote tree hangs from, the last activity, and the context gauge.
func TestRunsSnapshotToWire_CarriesLineageActivityAndContext(t *testing.T) {
	at := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	got := RunsSnapshotToWire(coord.RunsSnapshot{Runs: []coord.RunInfo{
		{RunID: "kid", ParentHarp: "root-harp", LastEventAt: at, ContextTokens: 1200, ContextWindow: 200000},
		{RunID: "quiet"},
	}}).GetRuns()
	require.Len(t, got, 2)
	assert.Equal(t, "root-harp", got[0].GetParentHarp())
	assert.Equal(t, at, got[0].GetLastEventAt().AsTime())
	assert.EqualValues(t, 1200, got[0].GetContextTokensUsed())
	assert.EqualValues(t, 200000, got[0].GetContextWindow())
	assert.Nil(t, got[1].GetLastEventAt(), "no activity is absent, never the zero time")
	assert.Zero(t, got[1].GetContextTokensUsed())
}

// A hold reaches the wire with its carrier's names and its deadline; a hold
// only the human releases carries none.
func TestCredentialHoldsToWire(t *testing.T) {
	since := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	got := CredentialHoldsToWire([]coord.CredentialHold{
		{Engine: "claude", Kind: agent.FailureRateLimited, Since: since, Until: since.Add(5 * time.Minute), Harps: []string{"a", "b"},
			Source: engine.CredentialSource{Key: "claude env=ANTHROPIC_API_KEY store=", EnvVars: []string{"ANTHROPIC_API_KEY"}}},
		{Engine: "claude", Kind: agent.FailureCredentialRejected, Since: since, Harps: []string{"c"}},
	}).GetHolds()
	require.Len(t, got, 2)
	assert.Equal(t, "claude", got[0].GetEngine())
	assert.Equal(t, string(agent.FailureRateLimited), got[0].GetKind())
	assert.Equal(t, "claude env=ANTHROPIC_API_KEY store=", got[0].GetSourceKey())
	assert.Equal(t, []string{"ANTHROPIC_API_KEY"}, got[0].GetSourceEnvVars())
	assert.Equal(t, since, got[0].GetSince().AsTime())
	assert.Equal(t, since.Add(5*time.Minute), got[0].GetUntil().AsTime())
	assert.Equal(t, []string{"a", "b"}, got[0].GetHarps())
	assert.Nil(t, got[1].GetUntil(), "a refused credential releases only when the human acts")
}
