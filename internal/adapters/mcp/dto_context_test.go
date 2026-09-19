package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
)

// The port exists so an internal caller's need CANNOT reach the wire. Asserted
// on the marshalled payload, and deliberately with a domain result that carries
// every field the wire must not see — a test that only checked the happy fields
// would pass while the leak it guards against shipped.
func TestAssembleContextDTO_DoesNotPublishInternalState(t *testing.T) {
	raw, err := json.Marshal(assembleContextDTO(&operations.AssembleContextResult{
		Profiles:        []string{"coordinator"},
		Context:         "ASSEMBLED",
		FragmentsLoaded: []string{"a#fragments/b"},

		// Internal routing: which backend a CLI launch should pick. This
		// already shipped to MCP callers before the port existed.
		ProfileLLM: "claude-code",
		// The menu now lives on ctxloom://fragments, where MCP puts descriptors.
		PremiseIndex: []operations.PremiseIndexEntry{{Name: "x#fragments/y", Premise: "when z"}},
		// The one that would have defeated the mechanism: shipping withheld
		// BODIES to the very callers that withheld them.
		WithheldFragments: []operations.WithheldFragment{
			{Name: "x#fragments/y", Premise: "when z", Content: "SECRET-WITHHELD-BODY"},
		},
	}))
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	require.Equal(t, "ASSEMBLED", got["context"], "the payload must still deliver what a caller came for")
	require.Contains(t, got, "profiles")
	require.Contains(t, got, "fragments_loaded")

	require.NotContains(t, got, "profile_llm", "backend selection is a CLI concern; an MCP caller neither chooses nor invokes one")
	require.NotContains(t, got, "premise_index", "the menu belongs on the descriptor side (ctxloom://fragments), not bolted onto an assembly")

	require.NotContains(t, string(raw), "SECRET-WITHHELD-BODY",
		"a withheld body reaching this response would inject it into exactly the callers that withheld it")
}

// A nil result must not become a JSON "null" body some client then dereferences.
func TestAssembleContextDTO_NilStaysNil(t *testing.T) {
	require.Nil(t, assembleContextDTO(nil))
}
