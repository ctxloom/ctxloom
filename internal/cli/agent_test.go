// Tests for cmd/agent.go's extracted renderers. The cobra wrappers route
// list/show through internal/operations (covered by operations/agents_test.go);
// the CLI-local testable surface is the formatting in renderAgentList /
// renderAgentShow.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestRenderAgentList_EngineAndDefault(t *testing.T) {
	list := []operations.AgentEntry{
		{Name: "dev", Engine: "claude-code", Profiles: []string{"go-developer", "go-style"}, Runtime: "container"},
		{Name: "finder", Profiles: []string{"finder"}}, // no engine, no isolation
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "Agents (2):")
	assert.Contains(t, out, "dev (engine: claude-code)")
	assert.Contains(t, out, "profiles: go-developer, go-style")
	assert.Contains(t, out, "runtime: container")
	assert.Contains(t, out, "finder (engine: project default)", "unset engine shows the project-default hint")
	assert.Equal(t, 1, strings.Count(out, "runtime:"), "unset runtime prints nothing (inherits the project default)")
}

// TestRenderAgentList_Driving proves a declared `driving:` value renders in
// the list, and an unset one prints nothing (mirroring Runtime/Coordinator's
// existing omit-when-empty convention).
func TestRenderAgentList_Driving(t *testing.T) {
	list := []operations.AgentEntry{
		{Name: "shooter", Engine: "claude-code", Driving: "oneshot"},
		{Name: "chatter", Engine: "claude-code"}, // driving unset
	}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "driving: oneshot")
	assert.Equal(t, 1, strings.Count(out, "driving:"), "unset driving prints nothing")
}

func TestRenderAgentList_Empty(t *testing.T) {
	var buf bytes.Buffer
	assert.NoError(t, renderAgentList(&buf, nil))
	assert.Contains(t, buf.String(), "No agents defined.")
}

func TestRenderAgentShow_Resolved(t *testing.T) {
	def := &operations.AgentEntry{Name: "dev", Engine: "slow", Profiles: []string{"p1", "p2"}, Runtime: "container", Source: "config"}
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
	assert.Contains(t, out, "Runtime: container")
	assert.Contains(t, out, "Resolved engine: slow (backend: mock, model: m-slow)")
	assert.Contains(t, out, "Composed fragments: 2")
}

// TestRenderAgentShow_Driving proves a declared `driving:` value renders in
// `agent show`.
func TestRenderAgentShow_Driving(t *testing.T) {
	def := &operations.AgentEntry{Name: "shooter", Engine: "slow", Driving: "oneshot", Source: "config"}
	resolved := &operations.ResolvedAgent{Name: "shooter", Label: "slow", Backend: "mock"}
	var buf bytes.Buffer
	assert.NoError(t, renderAgentShow(&buf, def, resolved, nil))
	assert.Contains(t, buf.String(), "Driving: oneshot")
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

// `agent show` resolves the engine fault-tolerantly: a failure (e.g. a missing
// constituent profile) still prints the definition, with the reason. The TEXT
// renderer says so — "Resolved engine: unavailable (…)" — but the --format json
// payload carried only `resolved` omitted, so a structured consumer saw an
// agent with no resolution and no way to learn why. Two formats of the same
// command must not disagree about whether anything went wrong.
func TestAgentShow_JSONCarriesTheResolutionFailure(t *testing.T) {
	cfg := setupEditProject(t)
	_, err := operations.SetAgent(config.NewManager(), cfg, operations.SetAgentRequest{
		Name:     "broken",
		Profiles: &[]string{"no-such-profile"},
	})
	require.NoError(t, err)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	cmd, out := formatCmd("json")
	cmd.SetContext(context.Background())
	require.NoError(t, agentShowCmd.RunE(cmd, []string{"broken"}))

	var payload map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &payload))
	require.Contains(t, payload, "resolution_error",
		"a resolution failure the text view reports must not vanish from --format json")
	assert.NotEmpty(t, payload["resolution_error"])

	// Text and JSON must agree: the text renderer reports the same failure.
	var text bytes.Buffer
	def, gerr := operations.GetAgent(cfg, "broken")
	require.NoError(t, gerr)
	require.NoError(t, renderAgentShow(&text, def, nil, errors.New("no-such-profile not found")))
	assert.Contains(t, text.String(), "Resolved engine: unavailable")
}

// The key is absent — not present-and-empty — when resolution succeeded, so a
// consumer can test for it directly.
func TestAgentShow_JSONOmitsTheErrorKeyWhenResolutionSucceeded(t *testing.T) {
	payload, err := json.Marshal(agentShowJSON{
		Definition: &operations.AgentEntry{Name: "dev"},
		Resolved:   &operations.ResolvedAgent{Label: "claude-code"},
	})
	require.NoError(t, err)
	assert.NotContains(t, string(payload), "resolution_error")
}
