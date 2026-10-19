package operations

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/core/engine"
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
	assert.Equal(t, "bypassPermissions", name("claude-code", block("claude-code", "bypassPermissions"), label), "the binding beats the label")
	assert.Empty(t, name("claude-code", block("mock", "bypass"), label), "no block for the engine")
	assert.Empty(t, name("claude-code", block("claude-code", "dontAsk"), label), "a refused declaration names nothing")
	assert.Empty(t, name("nope", agents.Permissions{}, label), "an unknown engine")

	assert.Equal(t, "plan mode", EffectivePosture(reg, "claude-code", agents.Permissions{}, label).Label,
		"the posture carries its engine's display name, which the roster shows")
	assert.Empty(t, EffectivePosture(reg, "nope", agents.Permissions{}, label).Label)
}

// rootOnly is an engine double that declares a Definition and nothing else:
// EngineBinary reads only Root(), so the embedded nil Engine is never
// reached (a call to any other method would panic, loudly).
type rootOnly struct {
	engine.Engine
	def engine.Definition
}

func (r rootOnly) Root() engine.Base { return engine.Base{Definition: r.def} }

// EngineBinary is the binary an engine's own grammar launches: its
// interactive grammar's, and for an engine that declares no interactive
// mode, the grammar it does declare — a structured-only engine still has a
// binary to probe and to find on PATH. The engines are doubles with a
// distinct binary per grammar, so the test does not depend on which shipped
// engine happens to be structured-only, and an answer from the wrong grammar
// cannot pass.
func TestEngineBinary_IsTheDeclaredGrammarsBinary(t *testing.T) {
	def := func(name string, cli ...engine.CLIGrammar) engine.Engine {
		return rootOnly{def: engine.Definition{Name: engine.Name(name), CLI: cli}}
	}
	reg, err := engine.NewRegistry(
		def("structured-only", engine.CLIGrammar{Mode: engine.Structured, Binary: "structured-bin"}),
		def("both",
			engine.CLIGrammar{Mode: engine.Structured, Binary: "structured-first-bin"},
			engine.CLIGrammar{Mode: engine.Interactive, Binary: "interactive-bin"}),
		def("no-grammar"),
	)
	require.NoError(t, err)

	assert.Equal(t, "structured-bin", EngineBinary(reg, "structured-only"),
		"an engine with no interactive mode launches its declared structured grammar's binary")
	assert.Equal(t, "interactive-bin", EngineBinary(reg, "both"),
		"the interactive grammar's binary wins, wherever it is declared")
	assert.Empty(t, EngineBinary(reg, "no-grammar"), "an engine that declares no grammar has no binary")
	assert.Empty(t, EngineBinary(reg, "no-such-engine"))
}

// Every shipped engine that declares a grammar has a binary to resolve.
func TestEngineBinary_ShippedEnginesHaveOne(t *testing.T) {
	reg := engines.Registry()
	for _, n := range reg.Names(nil) {
		e, _ := reg.Lookup(n)
		if len(e.Root().CLI) > 0 {
			assert.NotEmpty(t, EngineBinary(reg, string(n)), n)
		}
	}
}
