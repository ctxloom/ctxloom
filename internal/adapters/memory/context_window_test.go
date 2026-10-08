package memory

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/tokens"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

func windowOf(n int) func(context.Context) (int, error) {
	return func(context.Context) (int, error) { return n, nil }
}

// TestInputBudget_IsTheSmallerOfTheCeilingAndTheWindowLessItsReserve: the
// transcript budget of the one compaction call is
// min(SinglePassInputTokens, window − reserve), the reserve being the prompt
// and the essence it asks for. An unknown window (no func, or 0) keeps the
// ceiling; a window no larger than the reserve refuses.
func TestInputBudget_IsTheSmallerOfTheCeilingAndTheWindowLessItsReserve(t *testing.T) {
	prompt := strings.Repeat("p", 4_000) // 1,000 tokens
	const essenceChars = 8_000           // 2,000 tokens
	const reserve = 3_000
	for name, tc := range map[string]struct {
		window func(context.Context) (int, error)
		want   int
	}{
		"no window func":     {nil, SinglePassInputTokens},
		"unknown window":     {windowOf(0), SinglePassInputTokens},
		"large window":       {windowOf(1_000_000), SinglePassInputTokens},
		"exactly at ceiling": {windowOf(SinglePassInputTokens + reserve), SinglePassInputTokens},
		"128k slot":          {windowOf(131_072), 131_072 - reserve},
		"one over reserve":   {windowOf(reserve + 1), 1},
	} {
		t.Run(name, func(t *testing.T) {
			c := &Compactor{config: CompactionConfig{ContextWindow: tc.window, EssenceMaxChars: essenceChars}}
			got, err := c.inputBudget(context.Background(), prompt)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}

	t.Run("window within the reserve refuses", func(t *testing.T) {
		c := &Compactor{config: CompactionConfig{ContextWindow: windowOf(reserve), EssenceMaxChars: essenceChars}}
		_, err := c.inputBudget(context.Background(), prompt)
		require.ErrorIs(t, err, ErrContextWindowTooSmall)
	})
	t.Run("a failing window lookup is the compaction's error", func(t *testing.T) {
		boom := errors.New("distiller did not start")
		c := &Compactor{config: CompactionConfig{
			ContextWindow:   func(context.Context) (int, error) { return 0, boom },
			EssenceMaxChars: essenceChars,
		}}
		_, err := c.inputBudget(context.Background(), prompt)
		require.ErrorIs(t, err, boom)
	})
}

// TestCompact_DistillerWindowBoundsTheOneCall: a transcript that fits the
// fixed ceiling whole is still REDUCED when the distiller's window is
// smaller, so the call never carries more than the window holds.
func TestCompact_DistillerWindowBoundsTheOneCall(t *testing.T) {
	testsupport.Isolate(t)

	// ~60k tokens: under SinglePassInputTokens, over a 32k window.
	body := strings.Repeat("y", tokens.Budget(30_000))
	src := &mockSource{currentSession: &agent.Session{
		ID: "small-window-session",
		Entries: []agent.SessionEntry{
			{Type: agent.EntryTypeUser, Content: body},
			{Type: agent.EntryTypeAssistant, Content: body},
		},
	}}
	var calls, sawBytes int
	runner := &scriptedRunner{RunFunc: func(ctx context.Context, prompt string, stdout, stderr io.Writer) (int32, error) {
		calls++
		sawBytes = len(prompt)
		_, _ = stdout.Write([]byte("---\nsummary: reduced\n---\n\n### Open Items\n- none\n"))
		return 0, nil
	}}

	const window = 32_768
	recordOutputDir(t, "compactor-under-test")
	compactor, err := NewCompactor(afero.NewOsFs(), CompactionConfig{
		Source:        src,
		Run:           runnerOver(runner),
		ContextWindow: windowOf(window),
		OutputDir:     t.TempDir(),
		HarpName:      "compactor-under-test",
	})
	require.NoError(t, err)

	result, err := compactor.Compact(context.Background())
	require.NoError(t, err)
	assert.Equal(t, 1, calls, "still exactly one call")
	assert.True(t, result.InputReduced, "a transcript over the window's budget is reduced and says so")
	assert.LessOrEqual(t, tokens.EstimateBytes(sawBytes), window,
		"the call carried %d tokens, more than the distiller's %d-token window", tokens.EstimateBytes(sawBytes), window)
}

// TestCompact_WindowTooSmallRefusesBeforeAnyCall: a distiller whose window
// cannot hold the prompt and essence reserve is refused, not sent a call
// that must overflow.
func TestCompact_WindowTooSmallRefusesBeforeAnyCall(t *testing.T) {
	testsupport.Isolate(t)

	body := strings.Repeat("z", tokens.Budget(2_000))
	src := &mockSource{currentSession: &agent.Session{
		ID:      "tiny-window-session",
		Entries: []agent.SessionEntry{{Type: agent.EntryTypeUser, Content: body}, {Type: agent.EntryTypeAssistant, Content: body}},
	}}
	var calls int
	runner := &scriptedRunner{RunFunc: func(ctx context.Context, prompt string, stdout, stderr io.Writer) (int32, error) {
		calls++
		return 0, nil
	}}
	recordOutputDir(t, "compactor-under-test")
	compactor, err := NewCompactor(afero.NewOsFs(), CompactionConfig{
		Source:        src,
		Run:           runnerOver(runner),
		ContextWindow: windowOf(100),
		OutputDir:     t.TempDir(),
		HarpName:      "compactor-under-test",
	})
	require.NoError(t, err)

	_, err = compactor.Compact(context.Background())
	require.ErrorIs(t, err, ErrContextWindowTooSmall)
	assert.Zero(t, calls, "no compaction call is made against a window that cannot hold it")
}
