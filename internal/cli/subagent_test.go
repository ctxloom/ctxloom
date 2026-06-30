// Tests for cmd/subagent.go's extracted renderers. The cobra wrappers route
// list/show through internal/operations (covered by operations/subagents_test.go);
// the CLI-local testable surface is the formatting in renderSubagentList /
// renderSubagentShow.
package cli

import (
	"bytes"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

func TestRenderSubagentList_EngineAndDefault(t *testing.T) {
	list := []operations.SubagentEntry{
		{Name: "dev", Engine: "claude-code", Profiles: []string{"go-developer", "go-style"}},
		{Name: "finder", Profiles: []string{"finder"}}, // no engine
	}
	var buf bytes.Buffer
	assert.NoError(t, renderSubagentList(&buf, list))
	out := buf.String()

	assert.Contains(t, out, "Subagents (2):")
	assert.Contains(t, out, "dev (engine: claude-code)")
	assert.Contains(t, out, "profiles: go-developer, go-style")
	assert.Contains(t, out, "finder (engine: project default)", "unset engine shows the project-default hint")
}

func TestRenderSubagentList_Empty(t *testing.T) {
	var buf bytes.Buffer
	assert.NoError(t, renderSubagentList(&buf, nil))
	assert.Contains(t, buf.String(), "No subagents defined.")
}

func TestRenderSubagentShow_Resolved(t *testing.T) {
	def := &operations.SubagentEntry{Name: "dev", Engine: "slow", Profiles: []string{"p1", "p2"}, Source: "config"}
	resolved := &operations.ResolvedSubagent{
		Name: "dev", Label: "slow", Backend: "mock", Model: "m-slow",
		Fragments: []string{"a", "b"},
	}
	var buf bytes.Buffer
	assert.NoError(t, renderSubagentShow(&buf, def, resolved, nil))
	out := buf.String()

	assert.Contains(t, out, "Subagent: dev")
	assert.Contains(t, out, "Source: config")
	assert.Contains(t, out, "Engine (declared): slow")
	assert.Contains(t, out, "Resolved engine: slow (backend: mock, model: m-slow)")
	assert.Contains(t, out, "Composed fragments: 2")
}

func TestRenderSubagentShow_ResolutionFailureStillPrintsDefinition(t *testing.T) {
	def := &operations.SubagentEntry{Name: "dev", Profiles: []string{"missing"}}
	var buf bytes.Buffer
	assert.NoError(t, renderSubagentShow(&buf, def, nil, errors.New("profile missing not found")))
	out := buf.String()

	assert.Contains(t, out, "Subagent: dev")
	assert.Contains(t, out, "Engine (declared): (project default)")
	assert.Contains(t, out, "Resolved engine: unavailable (profile missing not found)")
}

// TestSubagentSetupCmd_EmitsPrompt proves `subagent setup` writes the
// agent-assisted setup prompt — the SCAN → DISCUSS → SET instructions the LLM
// follows — to stdout. The prompt is a markdown resource, so the assertions pin
// only the load-bearing, name-agnostic mechanics (not any role/lens names).
func TestSubagentSetupCmd_EmitsPrompt(t *testing.T) {
	var buf bytes.Buffer
	subagentSetupCmd.SetOut(&buf)
	t.Cleanup(func() { subagentSetupCmd.SetOut(nil) })

	assert.NoError(t, subagentSetupCmd.RunE(subagentSetupCmd, nil))
	out := buf.String()

	assert.Contains(t, out, "ctxloom llm list", "prompt scans engines at runtime")
	assert.Contains(t, out, "ctxloom profile list", "prompt scans profiles at runtime")
	assert.Contains(t, out, "ctxloom subagent set", "prompt writes bindings via subagent set")
	assert.Contains(t, out, "search_library", "prompt discovers the cr-* lenses from the library")
}
