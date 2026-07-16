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

// TestServe_PromptExpandsCommand_BlocksParity pins the B2/B4 merge invariant
// the coordinator caught: a naive keep-both merge of B2 (structural
// ContentBlocks intake) and B4 (command expansion) would expand `text` but
// leave `blocks` carrying the RAW, unexpanded "/code-review" — a
// ContentBlocks consumer (internal/acp's CLIENT-role driver, which is the
// path B2 exists to enable) would then run the engine on the UNEXPANDED
// invocation while a text-only backend got the real expansion, silently.
// This drives a prompt carrying a command PLUS an attached image (the case
// that makes it non-trivial: expansion must not discard the image, and must
// not leave the raw command text sitting alongside the expansion either)
// and asserts BOTH channels agree.
func TestServe_PromptExpandsCommand_BlocksParity(t *testing.T) {
	eng := newFakeEngine()
	eng.commands = &SessionCommands{
		Available: []CommandInfo{{Name: "code-review"}},
		Resolve: func(_ context.Context, name, rest string) (string, bool, error) {
			if name != "code-review" {
				return "", false, nil
			}
			return "# Code Review\nPlease review the following code.", true, nil
		},
	}
	go eng.pump()
	c := startServer(t, func(context.Context, OpenRequest) (*EngineChat, error) { return eng.chat(""), nil })
	sid := c.handshake("/proj")

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[
		{"type":"text","text":"/code-review"},
		{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}
	]}`)

	msg := eng.receiveMsg(t)
	require.Len(t, msg.ContentBlocks, 2, "the expanded text block + the preserved image block — never the raw text block left in place alongside it")
	assert.Equal(t, "text", msg.ContentBlocks[0].Kind)
	assert.Equal(t, msg.Text, msg.ContentBlocks[0].Text,
		"a ContentBlocks consumer must see the IDENTICAL expanded command a Text-only consumer does")
	assert.Contains(t, msg.Text, "Please review the following code.", "the command must actually be expanded, not merely forwarded")
	assert.NotContains(t, msg.Text, "/code-review", "the raw slash text must not survive in the flattened form")
	assert.NotContains(t, msg.ContentBlocks[0].Text, "/code-review", "...nor in the structured form")

	assert.Equal(t, "image", msg.ContentBlocks[1].Kind, "an attachment must survive a command expansion untouched")
	assert.Contains(t, string(msg.ContentBlocks[1].Raw), "aGVsbG8=", "the image's real bytes must not be discarded when a command riding alongside it expands")

	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
}

// TestServe_PromptUnmatchedSlashPassesThrough_BlocksUnchanged extends the
// unmatched-slash safety rule to the structured channel: a leading slash
// that matches no advertised command (a file path here) must leave BOTH
// msg.Text AND msg.ContentBlocks completely untouched — image included —
// exactly as if expandCommand had never run at all.
func TestServe_PromptUnmatchedSlashPassesThrough_BlocksUnchanged(t *testing.T) {
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

	id := c.send("session/prompt", `{"sessionId":"`+sid+`","prompt":[
		{"type":"text","text":"/etc/passwd holds secrets"},
		{"type":"image","data":"aGVsbG8=","mimeType":"image/png"}
	]}`)

	msg := eng.receiveMsg(t)
	require.Len(t, msg.ContentBlocks, 2)
	assert.Equal(t, "text", msg.ContentBlocks[0].Kind)
	assert.Equal(t, "/etc/passwd holds secrets", msg.ContentBlocks[0].Text,
		"an unmatched leading slash must leave the STRUCTURED text block untouched too, not just the flattened one")
	assert.Equal(t, "image", msg.ContentBlocks[1].Kind)
	assert.Contains(t, string(msg.ContentBlocks[1].Raw), "aGVsbG8=")

	eng.events <- agent.ChatEvent{Complete: &agent.TurnMeta{StopReason: "end_turn"}}
	resp, _ := c.waitResponse(id)
	require.Nil(t, resp.Error)
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
