package mcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
	"github.com/ctxloom/ctxloom/internal/contextmetrics"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// seedContextSeries writes n samples for harp, climbing by step percent per
// sample, and returns the percent of the newest one.
func seedContextSeries(t *testing.T, harp string, n int, start, step float64) float64 {
	t.Helper()
	base := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	pct := start
	for i := range n {
		require.NoError(t, contextmetrics.Append(harp, contextmetrics.Sample{
			TS:         base.Add(time.Duration(i) * time.Minute),
			Harp:       harp,
			ContextPct: pct,
			TokensUsed: int(pct * 10000),
			Window:     1000000,
		}))
		if i < n-1 {
			pct += step
		}
	}
	return pct
}

// TestContextStatus_ReturnsLatestAndTrendForTheCallingSession pins the two
// things the tool exists to deliver: the CURRENT level, and enough history to
// see which way it is moving.
//
// The session is resolved from the server's own identity, never from an
// argument — so this also pins that a caller is answered about ITSELF. A
// handler that read some other harp's series would report a stranger's
// occupancy as the caller's own, which is worse than reporting nothing.
func TestContextStatus_ReturnsLatestAndTrendForTheCallingSession(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "swift-amber-falcon"
	newest := seedContextSeries(t, harp, 5, 30, 5) // 30,35,40,45,50

	// A second session's series must not leak into the answer.
	seedContextSeries(t, "bold-crimson-thunder", 3, 90, 1)

	s := &ctxServer{self: coord.Identity{Harp: harp}}
	_, out, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{})
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.True(t, out.Available)
	assert.Equal(t, harp, out.Harp)
	require.NotNil(t, out.Latest)
	assert.Equal(t, newest, out.Latest.ContextPct, "latest is the most recent sample, not the oldest")
	assert.Equal(t, 50.0, newest)

	require.Len(t, out.Trend, 5)
	assert.Equal(t, 30.0, out.Trend[0].ContextPct, "trend runs oldest first")
	assert.Equal(t, 50.0, out.Trend[len(out.Trend)-1].ContextPct)
	for _, sample := range out.Trend {
		assert.Equal(t, harp, sample.Harp, "another session's samples must never appear here")
	}

	assert.Contains(t, out.Message, "50%", "the message states the measurement, not just that one exists")
}

// TestContextStatus_TrendIsBoundedAndDefaulted: the tool is consulted BECAUSE
// context is scarce, so its answer must never be able to grow without bound.
func TestContextStatus_TrendIsBoundedAndDefaulted(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "swift-amber-falcon"
	seedContextSeries(t, harp, 150, 1, 0.1)

	s := &ctxServer{self: coord.Identity{Harp: harp}}

	_, def, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{})
	require.NoError(t, err)
	assert.Len(t, def.Trend, defaultContextTrend, "an unasked-for trend stays small")

	_, capped, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{Trend: 10000})
	require.NoError(t, err)
	assert.Len(t, capped.Trend, maxContextTrend, "an over-large request is capped, not honoured")

	_, asked, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{Trend: 3})
	require.NoError(t, err)
	assert.Len(t, asked.Trend, 3)
}

// TestContextStatus_NoSamplesSaysSoInsteadOfReturningZero is the honesty
// contract, and it is asserted on the PAYLOAD rather than on the call
// succeeding.
//
// The failure this forbids is the house defect in its most dangerous form: an
// agent asking "how much room is left" and being told 0% used. That reads as
// "plenty" and is the opposite of the truth, which is that nothing is known.
// So the result must carry available=false, NO latest sample at all, and a
// message that names the likely cause.
func TestContextStatus_NoSamplesSaysSoInsteadOfReturningZero(t *testing.T) {
	testsupport.Isolate(t)

	s := &ctxServer{self: coord.Identity{Harp: "swift-amber-falcon"}}
	_, out, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{})
	require.NoError(t, err, "an absent series is a fact to report, not an error to raise")
	require.NotNil(t, out)

	assert.False(t, out.Available)
	assert.Nil(t, out.Latest, "no samples must mean NO latest — not a zero-valued one")
	assert.Empty(t, out.Trend)
	assert.Equal(t, noContextSamplesMsg, out.Message)
	assert.Contains(t, out.Message, "no samples yet")
	assert.Contains(t, out.Message, "statusline")
	assert.Contains(t, out.Message, "UNKNOWN",
		"the message must not let an absent measurement be read as a low one")
}

// TestContextStatus_NoIdentitySaysSoToo: without a harp the tool has no
// session to answer about, and must say that rather than read whatever series
// happens to exist.
func TestContextStatus_NoIdentitySaysSoToo(t *testing.T) {
	testsupport.Isolate(t)
	seedContextSeries(t, "bold-crimson-thunder", 3, 80, 1)

	s := &ctxServer{self: coord.Identity{}}
	_, out, err := s.handleContextStatus(context.Background(), nil, contextStatusInput{})
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.False(t, out.Available)
	assert.Nil(t, out.Latest)
	assert.Equal(t, noSessionIdentityMsg, out.Message)
}

// TestContextStatus_DescriptionStatesWhatItIsFor pins the one thing that makes
// the tool get CALLED. A description that merely names the fields leaves an
// agent to decide for itself whether a hunch is worth a tool call; this one has
// to say that measuring beats guessing, and has to warn that an absent reading
// is not a low one.
func TestContextStatus_DescriptionStatesWhatItIsFor(t *testing.T) {
	assert.Contains(t, contextStatusDesc, "Measure")
	assert.Contains(t, contextStatusDesc, "trend")
	assert.Contains(t, contextStatusDesc, "NO percentage",
		"the description must warn that no data is reported as absence, not as zero")
}
