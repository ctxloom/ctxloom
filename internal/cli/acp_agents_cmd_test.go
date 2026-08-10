package cli

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestBuildACPAgentEntries_DefaultPlusAgents: the plain agent leads, then
// one entry per agent whose args select it via --agent.
func TestBuildACPAgentEntries_DefaultPlusAgents(t *testing.T) {
	subs := []operations.AgentEntry{
		{Name: "docs", Profiles: []string{"d1", "d2"}},
		{Name: "reviewer", LLM: "fast", Profiles: []string{"review"}},
	}
	entries := buildACPAgentEntries(subs, "/usr/local/bin/ctxloom")
	require.Len(t, entries, 3)

	assert.Equal(t, "ctxloom", entries[0].Name)
	assert.Equal(t, "/usr/local/bin/ctxloom", entries[0].Command)
	assert.Equal(t, []string{"acp", "serve"}, entries[0].Args)
	assert.Empty(t, entries[0].Agent)

	assert.Equal(t, "ctxloom: docs", entries[1].Name)
	assert.Equal(t, []string{"acp", "serve", "--agent", "docs"}, entries[1].Args)
	assert.Equal(t, []string{"d1", "d2"}, entries[1].Profiles)

	assert.Equal(t, "ctxloom: reviewer", entries[2].Name)
	assert.Equal(t, "fast", entries[2].LLM)
}

// TestZedAgentServersBlock_ValidJSONStableOrder: the paste block is valid JSON
// keyed by entry name, in entry order (a Go map would randomize it).
func TestZedAgentServersBlock_ValidJSONStableOrder(t *testing.T) {
	entries := buildACPAgentEntries([]operations.AgentEntry{
		{Name: "reviewer", LLM: "fast"},
	}, "/bin/ctxloom")

	block := zedAgentServersBlock(entries)

	var parsed map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	require.NoError(t, json.Unmarshal([]byte(block), &parsed), "the block must be valid JSON: %s", block)
	require.Len(t, parsed, 2)
	assert.Equal(t, "/bin/ctxloom", parsed["ctxloom"].Command)
	assert.Equal(t, []string{"acp", "serve", "--agent", "reviewer"}, parsed["ctxloom: reviewer"].Args)

	// Stable order: the default entry renders before the agent entry.
	assert.Less(t, bytes.Index([]byte(block), []byte(`"ctxloom"`)), bytes.Index([]byte(block), []byte(`"ctxloom: reviewer"`)))
}

// TestRenderACPAgents_ListsEntriesAndZedBlock: the human output names every
// entry, its engine/profiles, and includes the ready-to-paste Zed block.
func TestRenderACPAgents_ListsEntriesAndZedBlock(t *testing.T) {
	entries := buildACPAgentEntries([]operations.AgentEntry{
		{Name: "reviewer", LLM: "fast", Profiles: []string{"r1", "r2"}},
		{Name: "docs", Profiles: []string{"d"}},
	}, "/bin/ctxloom")

	var buf bytes.Buffer
	require.NoError(t, renderACPAgents(&buf, entries))
	out := buf.String()

	assert.Contains(t, out, "ctxloom: reviewer")
	assert.Contains(t, out, "llm: fast")
	assert.Contains(t, out, "profiles: r1, r2")
	assert.Contains(t, out, "llm: (project default)")
	assert.Contains(t, out, "agent_servers")
	assert.Contains(t, out, `"args":["acp","serve","--agent","docs"]`)
}

// TestRenderACPAgents_NoAgents: with no agents the default entry still
// advertises, with a pointer to defining agents.
func TestRenderACPAgents_NoAgents(t *testing.T) {
	entries := buildACPAgentEntries(nil, "/bin/ctxloom")
	require.Len(t, entries, 1)

	var buf bytes.Buffer
	require.NoError(t, renderACPAgents(&buf, entries))
	assert.Contains(t, buf.String(), "ctxloom")
	assert.Contains(t, buf.String(), "agent")
}

// TestAcpAgentsCmd_FormatJSON_EmitsMachineReadableEntries proves the
// frontend-neutral connection facts (init-as-skill plan §6/§10②) are
// available as structured JSON via the repo's standard --format json
// convention (internal/cli/format.go's emit, the same mechanism every other
// command uses) — a consuming agent parses THIS, not the human/Zed-block
// text render. This exercises the real emit() path the command's RunE calls
// (buildACPAgentEntries -> emit), matching the global --format flag's
// existing json/text branching rather than inventing a parallel --json flag.
func TestAcpAgentsCmd_FormatJSON_EmitsMachineReadableEntries(t *testing.T) {
	entries := buildACPAgentEntries([]operations.AgentEntry{
		{Name: "docs", LLM: "fast", Profiles: []string{"d1", "d2"}},
		{Name: "reviewer"},
	}, "/usr/local/bin/ctxloom")

	cmd, buf := formatCmd(formatJSON)
	require.NoError(t, emit(cmd, entries, func() error {
		t.Fatal("text renderer must not run in json mode")
		return nil
	}))

	var got []acpAgentEntry
	require.NoError(t, json.Unmarshal(buf.Bytes(), &got), "output must be the entries as JSON: %s", buf.String())
	require.Len(t, got, 3)

	assert.Equal(t, "ctxloom", got[0].Name)
	assert.Equal(t, "/usr/local/bin/ctxloom", got[0].Command)
	assert.Equal(t, []string{"acp", "serve"}, got[0].Args)
	assert.Empty(t, got[0].Agent, "the default entry carries no agent name")

	assert.Equal(t, "ctxloom: docs", got[1].Name)
	assert.Equal(t, "docs", got[1].Agent, "machine consumer reads the agent name from this field")
	assert.Equal(t, "/usr/local/bin/ctxloom", got[1].Command)
	assert.Equal(t, []string{"acp", "serve", "--agent", "docs"}, got[1].Args, "exact command+args a client would configure")
	assert.Equal(t, "fast", got[1].LLM)
	assert.Equal(t, []string{"d1", "d2"}, got[1].Profiles)

	assert.Equal(t, "ctxloom: reviewer", got[2].Name)
	assert.Equal(t, []string{"acp", "serve", "--agent", "reviewer"}, got[2].Args)
	assert.Empty(t, got[2].LLM, "unset engine stays empty, not a synthesized default")
}

// zedAgentServersBlock's two discarded json.Marshal errors look like an
// ERRHANDLING defect. The discards are real, but the harm is not: encoding/json
// cannot fail for these operands. Both are built from string and []string only
// — no channels, funcs, complex values, cyclic pointers, NaN/Inf, or custom
// Marshalers, which are the only things Marshal reports — and invalid UTF-8 in
// a string is COERCED to U+FFFD rather than erroring.
//
// So this pins the property that makes the discard safe, over inputs chosen to
// break a hand-rolled quoter: the block is always parseable JSON, and it says
// exactly what the entries said. If someone widens acpAgentEntry with a type
// Marshal CAN fail on, this is what notices.
func TestZedAgentServersBlock_IsAlwaysValidJSON(t *testing.T) {
	entries := []acpAgentEntry{
		{Name: `quote"and\backslash`, Command: "/usr/bin/ctxloom", Args: []string{"acp", "serve"}},
		{Name: "tab\tand\nnewline", Command: "cmd with spaces", Args: nil},
		{Name: "unicode ✓ and <html> &  ", Command: "/bin/ctxloom", Args: []string{"--flag=<a&b>"}},
		{Name: "invalid-utf8-" + string([]byte{0xff, 0xfe}), Command: "x", Args: []string{string([]byte{0x80})}},
	}

	block := zedAgentServersBlock(entries)

	var parsed map[string]struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	require.NoError(t, json.Unmarshal([]byte(block), &parsed),
		"the hand-assembled block must always parse as JSON:\n%s", block)
	require.Len(t, parsed, len(entries))

	for _, e := range entries {
		// Marshal COERCES invalid UTF-8 to U+FFFD rather than erroring — that
		// coercion is exactly why there is no error to handle — so compare
		// against the same round trip, not the raw bytes.
		coercedName := jsonRoundTrip(t, e.Name)
		want := jsonRoundTrip(t, zedAgentServer{Command: e.Command, Args: e.Args})

		got, ok := parsed[coercedName]
		require.True(t, ok, "entry %q survived the round trip", e.Name)
		assert.Equal(t, want.Command, got.Command)
		assert.Equal(t, want.Args, got.Args)
	}
}

// jsonRoundTrip marshals v and unmarshals it back into its own type, yielding
// the value JSON preserves (with invalid UTF-8 already coerced).
func jsonRoundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err, "the pin's own expectation must marshal cleanly")
	var back T
	require.NoError(t, json.Unmarshal(data, &back))
	return back
}
