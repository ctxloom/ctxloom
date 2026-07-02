//go:build integration

package integration

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestACPAgent_SelfConformance drives `ctxloom acp` (the AGENT half) with
// ctxloom's OWN ACP client driver (the half that drives kiro/claude/codex) —
// both ends of the protocol conformance-tested against each other over a real
// subprocess boundary, fully hermetic via the mock engine: outer driver →
// `ctxloom acp` → plugin (mock) chat → echo → session/update → outer driver.
// The mock's echo proves exactly what the server delivered to the engine.
func TestACPAgent_SelfConformance(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 2)
	in <- agent.ChatMessage{Text: "conformance ping"}
	in <- agent.ChatMessage{Text: "second turn"}
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	var texts []string
	completes := 0
	for ev := range out {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			texts = append(texts, ev.Entry.Content)
		case ev.Complete != nil:
			completes++
		}
	}
	require.NoError(t, <-done, "the loopback ACP conversation must complete cleanly")

	require.Len(t, texts, 2, "one assistant entry per turn")
	assert.Contains(t, texts[0], "mock chat: ", "the mock engine's echo came back through both protocol halves")
	assert.Contains(t, texts[0], "conformance ping")
	assert.Equal(t, "mock chat: second turn", texts[1], "the second turn carries no context prefix")
	assert.Equal(t, 2, completes, "one completion marker per turn")
}

// TestACPAgent_PermissionPassThrough drives the WHOLE permission loop over
// real process + plugin boundaries: the mock engine raises a scripted
// permission request → `ctxloom acp` forwards it to its ACP client (the outer
// driver) as session/request_permission → the driver decides (allow under
// AutoApprove, reject otherwise) → the decision rides back down and the mock
// echoes the verdict.
func TestACPAgent_PermissionPassThrough(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	cases := []struct {
		name        string
		autoApprove bool
		want        string
	}{
		{"approved", true, "mock chat: permission granted"},
		{"rejected", false, "mock chat: permission denied"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp"})

			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			in := make(chan agent.ChatMessage, 1)
			in <- agent.ChatMessage{Text: "PERMISSION check"}
			close(in)
			out := make(chan agent.ChatEvent, 64)

			done := make(chan error, 1)
			go func() {
				done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir, AutoApprove: tc.autoApprove}, in, out)
			}()

			var texts []string
			for ev := range out {
				if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
					texts = append(texts, ev.Entry.Content)
				}
			}
			require.NoError(t, <-done)
			require.Len(t, texts, 1)
			assert.Equal(t, tc.want, texts[0], "the driver's decision must round-trip to the engine")
		})
	}
}

// TestACPAgent_PerTurnCancel proves the per-turn cancel seam across the whole
// stack: the mock engine parks on a HANG turn, the outer driver's CancelTurn
// message becomes session/cancel → the agent cancels only the TURN (stopReason
// cancelled), and the SAME session then answers a further turn.
func TestACPAgent_PerTurnCancel(t *testing.T) {
	env := setupTestEnv(t)
	_, err := env.SetupMockLM()
	require.NoError(t, err)

	drv := acp.NewChatDriver(acp.ACPConfig{Command: env.AppBinary + " acp"})

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 4)
	out := make(chan agent.ChatEvent, 64)
	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{WorkDir: env.ProjectDir}, in, out)
	}()

	in <- agent.ChatMessage{Text: "HANG until cancelled"}
	in <- agent.ChatMessage{CancelTurn: true}
	in <- agent.ChatMessage{Text: "still alive?"}
	close(in)

	var stops, texts []string
	for ev := range out {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			texts = append(texts, ev.Entry.Content)
		case ev.Complete != nil:
			stops = append(stops, ev.Complete.StopReason)
		}
	}
	require.NoError(t, <-done)

	require.Len(t, stops, 2, "the cancelled turn and the follow-up both complete")
	assert.Equal(t, "cancelled", stops[0], "the hung turn resolves with the spec's required stop reason")
	assert.Equal(t, "end_turn", stops[1])
	require.Len(t, texts, 1)
	assert.Contains(t, texts[0], "still alive?", "the session survives a per-turn cancel")
}
