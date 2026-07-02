package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestBuildACPAgentEntries_DefaultPlusSubagents: the plain agent leads, then
// one entry per subagent whose args select it via --subagent.
func TestBuildACPAgentEntries_DefaultPlusSubagents(t *testing.T) {
	subs := []operations.SubagentEntry{
		{Name: "docs", Profiles: []string{"d1", "d2"}},
		{Name: "reviewer", Engine: "fast", Profiles: []string{"review"}},
	}
	entries := buildACPAgentEntries(subs, "/usr/local/bin/ctxloom")
	require.Len(t, entries, 3)

	assert.Equal(t, "ctxloom", entries[0].Name)
	assert.Equal(t, "/usr/local/bin/ctxloom", entries[0].Command)
	assert.Equal(t, []string{"acp"}, entries[0].Args)
	assert.Empty(t, entries[0].Subagent)

	assert.Equal(t, "ctxloom: docs", entries[1].Name)
	assert.Equal(t, []string{"acp", "--subagent", "docs"}, entries[1].Args)
	assert.Equal(t, []string{"d1", "d2"}, entries[1].Profiles)

	assert.Equal(t, "ctxloom: reviewer", entries[2].Name)
	assert.Equal(t, "fast", entries[2].Engine)
}

// TestZedAgentServersBlock_ValidJSONStableOrder: the paste block is valid JSON
// keyed by entry name, in entry order (a Go map would randomize it).
func TestZedAgentServersBlock_ValidJSONStableOrder(t *testing.T) {
	entries := buildACPAgentEntries([]operations.SubagentEntry{
		{Name: "reviewer", Engine: "fast"},
	}, "/bin/ctxloom")

	block := zedAgentServersBlock(entries)

	var parsed map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	require.NoError(t, json.Unmarshal([]byte(block), &parsed), "the block must be valid JSON: %s", block)
	require.Len(t, parsed, 2)
	assert.Equal(t, "/bin/ctxloom", parsed["ctxloom"].Command)
	assert.Equal(t, []string{"acp", "--subagent", "reviewer"}, parsed["ctxloom: reviewer"].Args)

	// Stable order: the default entry renders before the subagent entry.
	assert.Less(t, bytes.Index([]byte(block), []byte(`"ctxloom"`)), bytes.Index([]byte(block), []byte(`"ctxloom: reviewer"`)))
}

// TestRenderACPAgents_ListsEntriesAndZedBlock: the human output names every
// entry, its engine/profiles, and includes the ready-to-paste Zed block.
func TestRenderACPAgents_ListsEntriesAndZedBlock(t *testing.T) {
	entries := buildACPAgentEntries([]operations.SubagentEntry{
		{Name: "reviewer", Engine: "fast", Profiles: []string{"r1", "r2"}},
		{Name: "docs", Profiles: []string{"d"}},
	}, "/bin/ctxloom")

	var buf bytes.Buffer
	require.NoError(t, renderACPAgents(&buf, entries))
	out := buf.String()

	assert.Contains(t, out, "ctxloom: reviewer")
	assert.Contains(t, out, "engine: fast")
	assert.Contains(t, out, "profiles: r1, r2")
	assert.Contains(t, out, "engine: (project default)")
	assert.Contains(t, out, "agent_servers")
	assert.Contains(t, out, `"args":["acp","--subagent","docs"]`)
}

// TestRenderACPAgents_NoSubagents: with no subagents the default entry still
// advertises, with a pointer to defining subagents.
func TestRenderACPAgents_NoSubagents(t *testing.T) {
	entries := buildACPAgentEntries(nil, "/bin/ctxloom")
	require.Len(t, entries, 1)

	var buf bytes.Buffer
	require.NoError(t, renderACPAgents(&buf, entries))
	assert.Contains(t, buf.String(), "ctxloom")
	assert.Contains(t, buf.String(), "subagent")
}
