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
