package acpagent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	api "github.com/coder/acp-go-sdk"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestToolContentFromIR_NonTextBlocksSurvive covers the outbound half of the
// "parse totally" work. A block with empty Text and populated Raw is the
// NORMAL shape for anything non-textual; emitting textBlock(c.Text) for those
// sent an empty block, indistinguishable from a tool returning nothing.
func TestToolContentFromIR_NonTextBlocksSurvive(t *testing.T) {
	t.Run("an ACP-origin block round-trips losslessly", func(t *testing.T) {
		// This is exactly what internal/acp/mapping.go stores in Raw.
		inner, err := json.Marshal(api.ToolCallContent{Content: &api.ToolCallContentContent{Content: textBlock("ROUNDTRIP")}})
		require.NoError(t, err)
		got := toolContentFromIR([]agent.ToolContentBlock{
			{Kind: agent.KindContent, Raw: inner},
		})
		require.Len(t, got, 1)
		require.NotNil(t, got[0].Content, "the ACP variant must be rebuilt, not flattened")
		blob, merr := json.Marshal(got[0])
		require.NoError(t, merr)
		assert.Contains(t, string(blob), "ROUNDTRIP")
	})

	t.Run("a vendor-origin block stays VISIBLE instead of going empty", func(t *testing.T) {
		// A claude tool_reference: real content, but not an ACP variant.
		got := toolContentFromIR([]agent.ToolContentBlock{
			{Kind: agent.KindToolCatalog, Raw: json.RawMessage(`{"tool_name":"SENTINEL-TOOL"}`)},
		})
		require.Len(t, got, 1, "a purpose-named kind must not be dropped outbound")
		require.NotNil(t, got[0].Content)
		blob, err := json.Marshal(got[0])
		require.NoError(t, err)
		assert.Contains(t, string(blob), "SENTINEL-TOOL",
			"the payload must reach the far client, not an empty text block")
	})

	t.Run("every new canonical kind maps to something", func(t *testing.T) {
		for _, k := range []string{
			agent.KindProcessOutput, agent.KindFileSnapshot,
			agent.KindToolCatalog, agent.KindAgentResult, agent.KindExcluded,
		} {
			got := toolContentFromIR([]agent.ToolContentBlock{{Kind: k, Text: "SENTINEL-" + k}})
			require.Len(t, got, 1, "kind %q was dropped outbound", k)
			blob, err := json.Marshal(got[0])
			require.NoError(t, err)
			assert.Contains(t, string(blob), "SENTINEL-"+k)
		}
	})
}
