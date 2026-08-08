package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestNewRecorder_AppliesContentPolicy is the WIRING test, and it exists
// because its absence let a real mutation survive: with the policy unit tests
// green and the adapter tests green, deleting the Filtered(...) wrap from
// NewRecorder changed no test outcome at all. Every transcript would then be
// written unfiltered — megabytes of shell output and whole-file snapshots —
// with nothing anywhere to indicate it.
//
// So this drives a REAL recorder to a REAL file and reads the bytes back.
// Asserting on Policy.Apply in isolation cannot make this claim: it proves
// the policy works, never that anything calls it.
func TestNewRecorder_AppliesContentPolicy(t *testing.T) {
	testsupport.Isolate(t)

	rec, err := NewRecorder("filtered-harp", "claude")
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolCallID: "call1",
		ToolOutput: "the flattened text",
		ToolContent: []agent.ToolContentBlock{
			{Kind: agent.KindContent, Text: "kept"},
			{Kind: agent.KindProcessOutput, Raw: json.RawMessage(`{"stdout":"SENTINEL-STDOUT"}`)},
			{Kind: agent.KindFileSnapshot, Raw: json.RawMessage(`{"originalFile":"SENTINEL-FILE"}`)},
		},
	}}))
	require.NoError(t, rec.Close())

	p, err := paths.HarpCanonicalTranscriptPath("filtered-harp")
	require.NoError(t, err)
	raw, err := os.ReadFile(p)
	require.NoError(t, err)

	// The payloads are gone from the FILE — the claim the policy actually
	// makes, and the one a unit test on Apply cannot make.
	assert.NotContains(t, string(raw), "SENTINEL-STDOUT",
		"process_output must not reach disk")
	assert.NotContains(t, string(raw), "SENTINEL-FILE",
		"file_snapshot must not reach disk")

	var rec0 Record
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	require.True(t, sc.Scan(), "one record must have been written")
	require.NoError(t, json.Unmarshal(sc.Bytes(), &rec0))

	require.NotNil(t, rec0.Entry)
	require.Len(t, rec0.Entry.ToolContent, 3,
		"withheld blocks are REPLACED by markers, never removed — a shorter "+
			"list would be indistinguishable from a tool returning less")
	assert.Equal(t, agent.KindContent, rec0.Entry.ToolContent[0].Kind)
	assert.Equal(t, agent.KindExcluded, rec0.Entry.ToolContent[1].Kind)
	assert.Equal(t, agent.KindExcluded, rec0.Entry.ToolContent[2].Kind)
	assert.Contains(t, string(rec0.Entry.ToolContent[1].Raw), agent.KindProcessOutput,
		"the marker must name what was withheld, so absence is never silent")
	assert.Contains(t, string(rec0.Entry.ToolContent[2].Raw), agent.KindFileSnapshot)

	// The flattened text is untouched: the policy governs structured content,
	// never the entry's own output string.
	assert.Equal(t, "the flattened text", rec0.Entry.ToolOutput)
}

// TestNewRecorder_KeepsUnfilteredKinds guards the opposite failure — a policy
// that excludes too much. A tool catalogue and an agent result are the tool's
// real output and must survive to disk intact.
func TestNewRecorder_KeepsUnfilteredKinds(t *testing.T) {
	testsupport.Isolate(t)

	rec, err := NewRecorder("kept-harp", "claude")
	require.NoError(t, err)
	require.NoError(t, rec.Record(agent.ChatEvent{Entry: &agent.SessionEntry{
		Type:       agent.EntryTypeToolResult,
		ToolCallID: "call1",
		ToolContent: []agent.ToolContentBlock{
			{Kind: agent.KindToolCatalog, Raw: json.RawMessage(`{"tool_name":"SENTINEL-CATALOG"}`)},
			{Kind: agent.KindAgentResult, Raw: json.RawMessage(`{"agentId":"SENTINEL-AGENT"}`)},
		},
	}}))
	require.NoError(t, rec.Close())

	p, err := paths.HarpCanonicalTranscriptPath("kept-harp")
	require.NoError(t, err)
	raw, err := os.ReadFile(p)
	require.NoError(t, err)

	assert.Contains(t, string(raw), "SENTINEL-CATALOG")
	assert.Contains(t, string(raw), "SENTINEL-AGENT")
}
