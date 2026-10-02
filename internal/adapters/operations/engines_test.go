package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/engines"
)

// EffectivePosture is what an unflagged run resolves to, named by the
// engine: the binding's block for the engine over the label's keys over the
// engine's default; a binding with blocks but none for the engine is one
// the launch refuses, and names nothing.
func TestEffectivePosture(t *testing.T) {
	reg := engines.Registry()
	block := func(e, m string) agents.Permissions {
		return agents.Permissions{Engines: map[string]map[string]any{e: {"mode": m}}}
	}
	label := agents.LabelPermissions{Engine: map[string]any{"mode": "plan"}}
	name := func(backend string, b agents.Permissions, l agents.LabelPermissions) string {
		return PostureName(reg, EffectivePosture(reg, backend, b, l))
	}
	assert.Equal(t, "acceptEdits", name("claude-code", agents.Permissions{}, agents.LabelPermissions{}), "the engine's default")
	assert.Equal(t, "plan", name("claude-code", agents.Permissions{}, label), "the label's keys")
	assert.Equal(t, "bypass", name("claude-code", block("claude-code", "bypass"), label), "the binding beats the label")
	assert.Empty(t, name("claude-code", block("mock", "bypass"), label), "no block for the engine")
	assert.Empty(t, name("claude-code", block("claude-code", "dontAsk"), label), "a refused declaration names nothing")
	assert.Empty(t, name("nope", agents.Permissions{}, label), "an unknown engine")

	assert.Equal(t, "plan mode", EffectivePosture(reg, "claude-code", agents.Permissions{}, label).Label,
		"the posture carries its engine's display name, which the roster shows")
	assert.Empty(t, EffectivePosture(reg, "nope", agents.Permissions{}, label).Label)
}
