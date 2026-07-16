package acpagent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestServe_AvailableCommandsUpdate is B4's (gap G5) headline proof: an
// editor connecting to `ctxloom acp` sees ctxloom's REAL commands — name and
// description carried straight through from SessionCommands.Available — as
// an available_commands_update notification, sent right after session/new
// (the response schema itself has no field for this, unlike modes/models).
func TestServe_AvailableCommandsUpdate(t *testing.T) {
	eng := newFakeEngine()
	eng.commands = &SessionCommands{
		Available: []CommandInfo{
			{Name: "code-review", Description: "Review code for issues"},
			{Name: "no-description", Description: ""},
		},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	resp, updates := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	require.Nil(t, resp.Error)

	type wireCommand struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	var found []wireCommand
	for _, u := range updates {
		var params struct {
			Update struct {
				AvailableCommands []wireCommand `json:"availableCommands"`
			} `json:"update"`
		}
		if err := json.Unmarshal(u.Params, &params); err != nil {
			continue
		}
		if params.Update.AvailableCommands != nil {
			found = params.Update.AvailableCommands
		}
	}
	require.NotNil(t, found, "expected an available_commands_update notification carrying ctxloom's real commands")
	require.Len(t, found, 2)
	assert.Equal(t, "code-review", found[0].Name)
	assert.Equal(t, "Review code for issues", found[0].Description)
	assert.Equal(t, "no-description", found[1].Name)
	assert.Empty(t, found[1].Description, "an unset description advertises empty, never fabricated prose")
}

// TestServe_AvailableCommandsUpdate_NoneConfigured: a session with no
// commands configured emits NO available_commands_update at all — silent
// absence, never an empty-but-present frame.
func TestServe_AvailableCommandsUpdate_NoneConfigured(t *testing.T) {
	eng := newFakeEngine()
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })

	c.waitResponse(c.send("initialize", `{"protocolVersion":1,"clientCapabilities":{}}`))
	_, updates := c.waitResponse(c.send("session/new", `{"cwd":"/proj","mcpServers":[]}`))
	for _, u := range updates {
		assert.NotContains(t, string(u.Params), "availableCommands", "no commands configured -> no available_commands_update frame")
	}
}

// TestServe_PromptExpandsCommand proves the inbound half of B4: a prompt
// beginning "/<name> <rest>" that matches one of the session's advertised
// commands is expanded to that command's content (with the trailing text
// appended) BEFORE it reaches the engine — the engine has no idea what
// ctxloom's own commands are.
func TestServe_PromptExpandsCommand(t *testing.T) {
	eng := newFakeEngine()
	eng.commands = &SessionCommands{
		Available: []CommandInfo{{Name: "code-review", Description: "Review code"}},
		Resolve: func(_ context.Context, name, rest string) (string, bool, error) {
			if name != "code-review" {
				return "", false, nil
			}
			text := "# Code Review\nPlease review the following code for bugs."
			if rest != "" {
				text += "\n\n" + rest
			}
			return text, true, nil
		},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	go func() {
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/code-review focus on auth"}]}`)

	msg := eng.receivedText(t)
	assert.Contains(t, msg, "Please review the following code for bugs.")
	assert.Contains(t, msg, "focus on auth")
	assert.NotContains(t, msg, "/code-review", "the raw slash invocation must not reach the engine once expanded")
}

// TestServe_PromptUnmatchedSlashPassesThrough proves the safety rule:
// leading-slash text that does NOT match any advertised command name is left
// COMPLETELY UNTOUCHED — most "/word ..." prompts are just user text (a file
// path here), and misinterpreting one would silently corrupt what the user
// actually typed.
func TestServe_PromptUnmatchedSlashPassesThrough(t *testing.T) {
	eng := newFakeEngine()
	eng.commands = &SessionCommands{
		Available: []CommandInfo{{Name: "code-review"}},
		Resolve: func(_ context.Context, name, rest string) (string, bool, error) {
			return "", false, nil // nothing ever matches in this test
		},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	go func() {
		eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	}()
	const original = "/etc/passwd contains secrets, please don't leak it"
	c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"`+original+`"}]}`)

	msg := eng.receivedText(t)
	assert.Equal(t, original, msg, "an unmatched leading slash must reach the engine byte-for-byte unchanged")
}

// TestServe_PromptCommandResolveError proves a MATCHED command whose
// resolution fails surfaces LOUDLY (a jsonrpc error on the prompt) rather
// than silently falling back to sending the raw, unexpanded slash text.
func TestServe_PromptCommandResolveError(t *testing.T) {
	eng := newFakeEngine()
	eng.commands = &SessionCommands{
		Available: []CommandInfo{{Name: "broken"}},
		Resolve: func(_ context.Context, name, rest string) (string, bool, error) {
			return "", true, errors.New("bundle vanished mid-session")
		},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[{"type":"text","text":"/broken now"}]}`)
	resp, _ := c.waitResponse(id)
	require.NotNil(t, resp.Error, "a matched command that fails to resolve must error the prompt, not silently pass raw text through")
	assert.Contains(t, resp.Error.Message, "bundle vanished mid-session")
}
