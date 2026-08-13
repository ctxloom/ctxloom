package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/contextmetrics"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// loadStatuslinePayload decodes one of the recorded statusline payloads in
// testdata/statusline.
//
// claude-2.1.229-no-usage.json is a VERBATIM live capture: the statusline
// command was wrapped to tee its stdin and a real Claude Code 2.1.229 session
// was driven until it rendered. It is kept unedited because its
// `used_percentage: null` (with `current_usage: null` beside it) is the exact
// state this code must not report as 0%, and a hand-written fixture would be
// an assumption about that state rather than a record of it.
//
// claude-2.1.229-in-use.json is the same payload with the context_window
// object populated as 2.1.229's own builder populates it (verified against the
// shipped bundle: total_input_tokens = input + cache_creation + cache_read,
// used_percentage an integer 0..100). The live capture could only be taken
// before the session accumulated usage, so the loaded half is reconstructed
// from the constructor rather than observed.
//
// Read through pkgSourceDir rather than a relative path: TestMain sandboxes
// the binary into a temp cwd, where "testdata/..." resolves to nothing.
func loadStatuslinePayload(t *testing.T, name string) agentSessionJSON {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(pkgSourceDir(t), "testdata", "statusline", name))
	require.NoError(t, err)
	var s agentSessionJSON
	require.NoError(t, json.Unmarshal(raw, &s))
	return s
}

// TestAgentSession_ParsesRealStatuslinePayload pins the field mapping against
// the recorded payloads. It is the test a wrong json tag on the context-window
// fields fails: every value below is read out of the fixture, so renaming
// `used_percentage`, `total_input_tokens` or `context_window_size` — or
// pointing the percentage at `remaining_percentage`, which sits beside it and
// would silently invert the reading — leaves this red.
func TestAgentSession_ParsesRealStatuslinePayload(t *testing.T) {
	t.Run("in_use", func(t *testing.T) {
		s := loadStatuslinePayload(t, "claude-2.1.229-in-use.json")
		require.NotNil(t, s.ContextWindow.UsedPercentage)
		assert.Equal(t, 37.0, *s.ContextWindow.UsedPercentage, "used_percentage, not remaining_percentage (63)")
		assert.Equal(t, 372000, s.ContextWindow.TotalInputTokens)
		assert.Equal(t, 1000000, s.ContextWindow.ContextWindowSize)
		assert.Equal(t, "e9093f97-709a-4580-8699-23367665aaa5", s.SessionID)
		assert.Equal(t, "Fable 5", s.modelName())
	})

	t.Run("no_usage_yet_is_null_not_zero", func(t *testing.T) {
		s := loadStatuslinePayload(t, "claude-2.1.229-no-usage.json")
		assert.Nil(t, s.ContextWindow.UsedPercentage,
			"a fresh session reports null; decoding it into a bare float64 would make it 0 and read as an empty context")
		assert.Equal(t, 1000000, s.ContextWindow.ContextWindowSize,
			"the window size is known even when occupancy is not")
	})
}

// TestContextSample_RefusesToInventAMeasurement covers the honesty gate on the
// capture side. Neither refusal may be softened into a zero-valued sample: a
// recorded 0% is indistinguishable from a genuinely empty context, so an
// unmeasured session must produce NO sample at all.
func TestContextSample_RefusesToInventAMeasurement(t *testing.T) {
	now := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)

	t.Run("no_percentage_no_sample", func(t *testing.T) {
		s := loadStatuslinePayload(t, "claude-2.1.229-no-usage.json")
		got, ok := contextSample(s, "swift-amber-falcon", now)
		assert.False(t, ok, "an unmeasured context must not be recorded as 0%%")
		assert.Zero(t, got.ContextPct)
	})

	t.Run("no_harp_no_sample", func(t *testing.T) {
		s := loadStatuslinePayload(t, "claude-2.1.229-in-use.json")
		_, ok := contextSample(s, "", now)
		assert.False(t, ok, "a sample attributed to no session has nowhere to go")
	})

	t.Run("measured_session_yields_the_payload", func(t *testing.T) {
		s := loadStatuslinePayload(t, "claude-2.1.229-in-use.json")
		got, ok := contextSample(s, "swift-amber-falcon", now)
		require.True(t, ok)
		assert.Equal(t, now, got.TS)
		assert.Equal(t, "swift-amber-falcon", got.Harp)
		assert.Equal(t, "e9093f97-709a-4580-8699-23367665aaa5", got.SessionID)
		assert.Equal(t, 37.0, got.ContextPct)
		assert.Equal(t, 372000, got.TokensUsed)
		assert.Equal(t, 1000000, got.Window)
		assert.Equal(t, "Fable 5", got.Model)
	})
}

// TestRecordContextSample_WritesTheSeries drives the statusline's own entry
// point end to end and asserts on the FILE — the bytes a later context_status
// call will read — rather than on the absence of an error, which this
// deliberately silent path never reports anyway.
func TestRecordContextSample_WritesTheSeries(t *testing.T) {
	testsupport.Isolate(t)
	const harp = "swift-amber-falcon"
	t.Setenv("CTXLOOM_SESSION_HARP", harp)

	recordContextSample(loadStatuslinePayload(t, "claude-2.1.229-in-use.json"))

	samples, err := contextmetrics.Read(harp)
	require.NoError(t, err)
	require.Len(t, samples, 1, "the statusline must actually land a sample on disk")
	assert.Equal(t, 37.0, samples[0].ContextPct)
	assert.Equal(t, 1000000, samples[0].Window)

	// The unmeasured payload adds nothing — the series stays at one sample
	// rather than gaining a 0% one.
	recordContextSample(loadStatuslinePayload(t, "claude-2.1.229-no-usage.json"))
	samples, err = contextmetrics.Read(harp)
	require.NoError(t, err)
	require.Len(t, samples, 1)
	assert.Equal(t, 37.0, samples[0].ContextPct)
}

// TestRecordContextSample_WithoutAHarpWritesNothing: outside a ctxloom session
// the statusline still renders, and it must not scatter metrics files.
func TestRecordContextSample_WithoutAHarpWritesNothing(t *testing.T) {
	home := testsupport.Isolate(t)
	t.Setenv("CTXLOOM_SESSION_HARP", "")

	recordContextSample(loadStatuslinePayload(t, "claude-2.1.229-in-use.json"))

	sessions := filepath.Join(home, ".ctxloom", "sessions")
	_, err := os.Stat(sessions)
	assert.True(t, os.IsNotExist(err), "no harp must mean no session state written at all")
}

// TestFormatHud_NullPercentageRendersNoBar: the null the engine sends before a
// session accumulates usage must render as it always did — nothing — rather
// than as a 0% bar claiming a measurement nobody took.
func TestFormatHud_NullPercentageRendersNoBar(t *testing.T) {
	testsupport.Isolate(t)
	s := loadStatuslinePayload(t, "claude-2.1.229-no-usage.json")
	out := formatHud(s, ctxloomHudInfo{})
	assert.NotContains(t, out, "0%")
	assert.NotContains(t, out, "░")
}
