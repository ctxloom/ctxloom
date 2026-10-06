package scriptedchat

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// events is roomy enough that no turn here blocks on relaying.
func events() chan engine.Event { return make(chan engine.Event, 64) }

func TestTurn_SignalsEnteredOnceBeforeAnswering(t *testing.T) {
	entered := make(chan struct{}, 2)
	chat := &Chat{Entered: entered}

	res, err := chat.Turn(context.Background(), engine.Exec{}, engine.Turn{Prompt: "hi"}, events())

	require.NoError(t, err)
	assert.Equal(t, "echo: hi", res.Answer)
	assert.Len(t, entered, 1, "Entered is told exactly once for the turn")
}

func TestTurn_GivesUpSignallingEnteredWhenCtxEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chat := &Chat{Entered: make(chan struct{})} // nobody receives

	res, err := chat.Turn(ctx, engine.Exec{}, engine.Turn{Prompt: "hi"}, events())

	require.ErrorIs(t, err, context.Canceled)
	assert.Empty(t, res.Answer, "a turn that never signalled Entered answers nothing")
}

func TestTurn_ConsultsFailedOncePerTurn(t *testing.T) {
	var prompts []string
	chat := &Chat{Failed: func(_ engine.Exec, prompt string) *agent.TurnFailure {
		prompts = append(prompts, prompt)
		return nil
	}}

	for _, p := range []string{"one", "two"} {
		_, err := chat.Turn(context.Background(), engine.Exec{}, engine.Turn{Prompt: p}, events())
		require.NoError(t, err)
	}
	assert.Equal(t, []string{"one", "two"}, prompts)
}
