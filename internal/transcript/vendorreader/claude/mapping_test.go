// Pure-function and small-fixture tests for content-block shapes
// claude_test.go's main real-shaped fixture cannot exercise: sidechain
// propagation (the interrupted session that fixture is built from never left
// its own main thread — subagent transcripts live in a SEPARATE
// subagents/<id>.jsonl file entirely, confirmed by sampling this box's
// captured sessions), a tool_result whose OWN content is an array (rather
// than a bare string) with tool_reference/image elements alongside text, and
// the empty-thinking / empty-content-array degenerate cases. Where a real
// captured shape is available it is used, shortened; the few purely
// structural cases (empty inputs) need no real source at all. See
// testdata/MANIFEST.json for exactly which literal here traces to a real
// capture.
package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

func TestDecodeContentBlocks(t *testing.T) {
	t.Run("bare string wraps as a single text block", func(t *testing.T) {
		blocks := decodeContentBlocks(json.RawMessage(`"hello there"`))
		require.Len(t, blocks, 1)
		assert.Equal(t, "text", blocks[0].Type)
		assert.Equal(t, "hello there", blocks[0].Text)
	})

	t.Run("empty string yields no blocks", func(t *testing.T) {
		assert.Nil(t, decodeContentBlocks(json.RawMessage(`""`)))
	})

	t.Run("array of blocks decodes directly", func(t *testing.T) {
		blocks := decodeContentBlocks(json.RawMessage(`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`))
		require.Len(t, blocks, 2)
		assert.Equal(t, "a", blocks[0].Text)
		assert.Equal(t, "b", blocks[1].Text)
	})

	t.Run("empty raw yields nil", func(t *testing.T) {
		assert.Nil(t, decodeContentBlocks(nil))
	})

	t.Run("malformed raw yields nil, not an error", func(t *testing.T) {
		assert.Nil(t, decodeContentBlocks(json.RawMessage(`{not valid`)))
	})
}

func TestMessageEntries_IsMetaUserSkipped(t *testing.T) {
	// Shortened real shape: claude's own <local-command-caveat> wrapper,
	// captured verbatim as an isMeta:true "user" line on this box (see
	// testdata/MANIFEST.json).
	blocks := decodeContentBlocks(json.RawMessage(`"<local-command-caveat>Caveat: ...</local-command-caveat>"`))
	evs := messageEntries("user", true, blocks, nil, nil)
	assert.Nil(t, evs, "isMeta:true user content must never become a canonical entry")
}

func TestMessageEntries_MultipleTextBlocksJoined(t *testing.T) {
	blocks := []contentBlock{
		{Type: "text", Text: "part one"},
		{Type: "text", Text: "part two"},
	}
	evs := messageEntries("user", false, blocks, nil, nil)
	require.Len(t, evs, 1)
	assert.Equal(t, "part one\n\npart two", evs[0].Entry.Content)
}

func TestMessageEntries_EmptyThinkingSkipped(t *testing.T) {
	// Real claude thinking blocks are routinely emitted with an EMPTY
	// thinking field and only a signature (confirmed on multiple real
	// captures on this box) — this must contribute nothing, never a blank
	// entry.
	evs := messageEntries("assistant", false, []contentBlock{{Type: "thinking", Thinking: ""}}, nil, nil)
	assert.Nil(t, evs)
}

func TestMessageEntries_EmptyContentSkipped(t *testing.T) {
	evs := messageEntries("user", false, nil, nil, nil)
	assert.Nil(t, evs)
}

func TestMessageEntries_ToolUseEmptyInputOmitsToolInput(t *testing.T) {
	evs := messageEntries("assistant", false, []contentBlock{{Type: "tool_use", ID: "t1", Name: "NoArgsTool"}}, nil, nil)
	require.Len(t, evs, 1)
	assert.Nil(t, evs[0].Entry.ToolInput)
}

func TestMessageEntries_ToolUseInputPassesThroughAsObject(t *testing.T) {
	// Unlike codex's function_call.arguments (a JSON-ENCODED STRING), claude's
	// tool_use.input is a real JSON object already — confirmed against real
	// tool_use blocks on this box — so it needs no second unmarshal.
	evs := messageEntries("assistant", false, []contentBlock{{
		Type: "tool_use", ID: "t1", Name: "Glob", Input: json.RawMessage(`{"pattern":"*.go"}`),
	}}, nil, nil)
	require.Len(t, evs, 1)
	assert.JSONEq(t, `{"pattern":"*.go"}`, string(evs[0].Entry.ToolInput))
}

func TestToolResultContent(t *testing.T) {
	t.Run("bare string content passes through", func(t *testing.T) {
		text, blocks := toolResultContent(json.RawMessage(`"plain output"`))
		assert.Equal(t, "plain output", text)
		require.Len(t, blocks, 1)
		assert.Equal(t, agent.KindContent, blocks[0].Kind)
	})

	t.Run("EVERY element survives; text still joins", func(t *testing.T) {
		// Shaped from a real tool_result content array on this box (an MCP
		// tool-listing result whose elements were type text/tool_reference;
		// image elements were also observed elsewhere, at far lower
		// frequency — see testdata/MANIFEST.json), values shortened.
		//
		// This is the anti-regression for the defect that motivated the whole
		// parse-totally/filter-deliberately split: the predecessor
		// (toolResultText) kept only the two text elements and tallied the
		// other two as having "no canonical representation". Measured across
		// 1030 real transcripts, that discarded 677 tool_reference + 1 image
		// elements and flattened 385 tool_result entries to COMPLETELY EMPTY.
		raw := json.RawMessage(`[
			{"type":"text","text":"first line"},
			{"type":"tool_reference","tool_name":"mcp__ctxloom__agent_run"},
			{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
			{"type":"text","text":"second line"}
		]`)
		text, blocks := toolResultContent(raw)
		assert.Equal(t, "first line\n\nsecond line", text)

		require.Len(t, blocks, 4, "every element must become a block, not just the text ones")
		assert.Equal(t, agent.KindToolCatalog, blocks[1].Kind, "tool_reference is a tool catalogue")
		assert.Equal(t, agent.KindContent, blocks[2].Kind, "an unmodeled element keeps the generic kind")

		// The bytes, not merely a block of the right shape: a block whose Raw
		// was dropped is the same information loss wearing a Kind.
		assert.JSONEq(t, `{"type":"tool_reference","tool_name":"mcp__ctxloom__agent_run"}`,
			string(blocks[1].Raw))
		assert.Contains(t, string(blocks[2].Raw), "aGVsbG8=",
			"the image's own bytes must survive verbatim in Raw")
	})

	t.Run("empty content yields no text and no blocks", func(t *testing.T) {
		text, blocks := toolResultContent(nil)
		assert.Equal(t, "", text)
		assert.Nil(t, blocks)
	})
}

func TestToolUseResultBlocks_ClassifiesByShape(t *testing.T) {
	// Every shape below is one measured at scale in this project's own
	// transcripts; the counts are from the last 40 sessions. Classification is
	// by which KEYS are present, never by tool name — the converter keeps no
	// tool_use_id -> name map, and a shape survives a tool being renamed.
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{"Bash process output (~8883 records)",
			`{"stdout":"hi","stderr":"","interrupted":false,"isImage":false,"noOutputExpected":false}`,
			agent.KindProcessOutput},
		{"Edit whole-file snapshot (~1581 records)",
			`{"filePath":"/a.go","originalFile":"before","structuredPatch":[],"userModified":false}`,
			agent.KindFileSnapshot},
		{"ToolSearch catalogue (282 records)",
			`{"matches":[],"query":"select:Read","total_deferred_tools":91}`,
			agent.KindToolCatalog},
		{"delegated agent result (~1181 records)",
			`{"agentId":"a1","status":"completed","resolvedModel":"claude-opus-5","prompt":"go"}`,
			agent.KindAgentResult},
		{"Read result carries no discriminator, so it is kept as content",
			`{"file":{"filePath":"/a.go"},"type":"text"}`,
			agent.KindContent},
		{"an unrecognized object keeps its bytes under the generic kind",
			`{"somethingClaudeShipsNextYear":true}`,
			agent.KindContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := toolUseResultBlocks(json.RawMessage(tc.raw))
			require.Len(t, blocks, 1)
			assert.Equal(t, tc.want, blocks[0].Kind)
			assert.JSONEq(t, tc.raw, string(blocks[0].Raw), "the vendor bytes must survive verbatim")
		})
	}

	t.Run("a scalar toolUseResult is still content, not a dropped record", func(t *testing.T) {
		// 2297 bare strings + 41 arrays measured: no shape to classify, but
		// the bytes are real output.
		blocks := toolUseResultBlocks(json.RawMessage(`"just a string"`))
		require.Len(t, blocks, 1)
		assert.Equal(t, agent.KindContent, blocks[0].Kind)
		assert.JSONEq(t, `"just a string"`, string(blocks[0].Raw))
	})

	t.Run("absent toolUseResult contributes nothing", func(t *testing.T) {
		assert.Nil(t, toolUseResultBlocks(nil))
	})
}

func TestMessageEntries_ToolResultFoldsLineToolUseResult(t *testing.T) {
	// The line's top-level toolUseResult belongs to the tool_result block on
	// that same line (verified 1:1 across 15821 real lines), so it must land
	// on THAT entry rather than becoming an entry of its own.
	evs := messageEntries("user", false, []contentBlock{{
		Type: "tool_result", ToolUseID: "call1",
		Content: json.RawMessage(`"exit 0"`),
	}}, json.RawMessage(`{"stdout":"exit 0","stderr":"","interrupted":false}`), nil)

	require.Len(t, evs, 1, "toolUseResult must fold in, not add an entry")
	require.Len(t, evs[0].Entry.ToolContent, 2)
	assert.Equal(t, agent.KindContent, evs[0].Entry.ToolContent[0].Kind)
	assert.Equal(t, agent.KindProcessOutput, evs[0].Entry.ToolContent[1].Kind)
	assert.Equal(t, "exit 0", evs[0].Entry.ToolOutput, "the flattened text is unaffected")
}

func TestMessageEntries_ToolResultIsError(t *testing.T) {
	evs := messageEntries("user", false, []contentBlock{{
		Type: "tool_result", ToolUseID: "call1", IsError: true,
		Content: json.RawMessage(`"the tool call failed"`),
	}}, nil, nil)
	require.Len(t, evs, 1)
	assert.True(t, evs[0].Entry.IsError)
	assert.Equal(t, "the tool call failed", evs[0].Entry.ToolOutput)
	assert.Equal(t, "call1", evs[0].Entry.ToolCallID)
}

func TestMessageEntries_UnmodeledBlockTypeSkipped(t *testing.T) {
	// A top-level image block pasted directly into a user turn — observed,
	// but rare, on this box; this schema has no field for raw image bytes
	// yet (agent.ContentBlock's richer projection is unpopulated by this
	// adapter), so it is dropped rather than fabricated into text.
	evs := messageEntries("user", false, []contentBlock{{Type: "image"}}, nil, nil)
	assert.Nil(t, evs)
}

// TestConvert_SidechainPropagates feeds a small fixture shaped from a real
// subagent transcript (claude writes a Task-tool child's own conversation to
// a SEPARATE <session>/subagents/<agentId>.jsonl file, every line of which
// carries isSidechain:true — confirmed on this box; see
// testdata/MANIFEST.json) and asserts agent.SessionEntry.Sidechain survives
// onto the recorded entry. claude_test.go's main fixture cannot cover this:
// it is drawn from a MAIN-thread session, which never sets isSidechain true.
func TestConvert_SidechainPropagates(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "subagent-fixture.jsonl")
	content := `{"parentUuid":"cd204eb8-f3be-4ff3-920b-1af649ab50d3","isSidechain":true,"agentId":"a0f0c165cd7295cfd","type":"assistant","sessionId":"10d9f2b3-7e75-4794-8fea-54b4143dbf6d","message":{"model":"claude-sonnet-5","id":"msg_011CcvjsTSfkyMHnhm24XiS9","type":"message","role":"assistant","content":[{"type":"tool_use","id":"toolu_01SzhJiYMoBU2crhmMSvmHyW","name":"Bash","input":{"command":"grep -rn SessionSource internal/lm/grpc/*.go"}}],"stop_reason":null,"usage":{"input_tokens":2,"cache_creation_input_tokens":8395,"cache_read_input_tokens":15168,"output_tokens":46}}}` + "\n"
	require.NoError(t, os.WriteFile(src, []byte(content), 0o644))

	rec, err := transcript.NewRecorder(fixtureHarp, "claude")
	require.NoError(t, err)
	require.NoError(t, Adapter{}.Convert(context.Background(), rec, src))
	require.NoError(t, rec.Close())

	path, err := paths.HarpCanonicalTranscriptPath(fixtureHarp)
	require.NoError(t, err)
	recs := readRecords(t, path)

	var sawEntry bool
	for _, r := range recs {
		if r.Kind != transcript.KindEntry {
			continue
		}
		require.NotNil(t, r.Entry)
		assert.True(t, r.Entry.Sidechain, "isSidechain:true on the source line must propagate to the recorded entry")
		sawEntry = true
	}
	assert.True(t, sawEntry, "expected the tool_use entry from the subagent line")
}
