// Tests for cmd/agent.go's extracted renderers. The cobra wrappers route
// list/show through internal/operations (covered by operations/agents_test.go);
// the CLI-local testable surface is the formatting in renderAgentList /
// renderAgentShow.
package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestRenderAgentList_EngineAndDefault(t *testing.T) {
	list := []operations.AgentEntry{
		{Name: "dev", Engine: "claude-code", Profiles: []string{"go-developer", "go-style"}},
		{Name: "finder", Profiles: []string{"finder"}}, // no engine
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "Agents (2):")
	assert.Contains(t, out, "dev (engine: claude-code)")
	assert.Contains(t, out, "profiles: go-developer, go-style")
	assert.Contains(t, out, "finder (engine: project default)", "unset engine shows the project-default hint")
}

func TestRenderAgentList_Empty(t *testing.T) {
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, nil))
	assert.Contains(t, buf.String(), "No agents defined.")
}

func TestRenderAgentShow_Resolved(t *testing.T) {
	def := &operations.AgentEntry{Name: "dev", Engine: "slow", Profiles: []string{"p1", "p2"}, Source: "config"}
	resolved := &operations.ResolvedAgent{
		Name: "dev", Label: "slow", Backend: "mock", Model: "m-slow",
		Fragments: []string{"a", "b"},
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, resolved, nil))
	out := buf.String()

	assert.Contains(t, out, "Agent: dev")
	assert.Contains(t, out, "Source: config")
	assert.Contains(t, out, "Engine (declared): slow")
	assert.Contains(t, out, "Resolved engine: slow (backend: mock, model: m-slow)")
	assert.Contains(t, out, "Composed fragments: 2")
}

func TestRenderAgentShow_ResolutionFailureStillPrintsDefinition(t *testing.T) {
	def := &operations.AgentEntry{Name: "dev", Profiles: []string{"missing"}}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, nil, errors.New("profile missing not found")))
	out := buf.String()

	assert.Contains(t, out, "Agent: dev")
	assert.Contains(t, out, "Engine (declared): (project default)")
	assert.Contains(t, out, "Resolved engine: unavailable (profile missing not found)")
}

// TestAgentSetupCmd_EmitsPrompt proves `agent setup` writes the
// agent-assisted setup prompt — the SCAN → DISCUSS → SET instructions the LLM
// follows — to stdout. The prompt is a markdown resource, so the assertions pin
// only the load-bearing, name-agnostic mechanics (not any role/lens names).
func TestAgentSetupCmd_EmitsPrompt(t *testing.T) {
	var buf bytes.Buffer
	agentSetupCmd.SetOut(&buf)
	t.Cleanup(func() { agentSetupCmd.SetOut(nil) })

	assert.NoError(t, agentSetupCmd.RunE(agentSetupCmd, nil))
	out := buf.String()

	assert.Contains(t, out, "ctxloom llm list", "prompt scans engines at runtime")
	assert.Contains(t, out, "ctxloom profile list", "prompt scans profiles at runtime")
	assert.Contains(t, out, "ctxloom agent set", "prompt writes bindings via agent set")
	assert.Contains(t, out, "search_library", "prompt discovers the cr-* lenses from the library")
}
