package acp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// U012-F04: a ChatMessage carrying no Text and no ContentBlocks used to be
// delivered as a single TextBlock("") — a turn that runs on zero bytes and
// returns a normal completion. Delivering nothing must be a loud failure, not
// a successful empty turn.
func TestBuildPromptBlocksRejectsEmptyMessage(t *testing.T) {
	s := &chatSession{}
	for _, tc := range []struct {
		name string
		msg  agent.ChatMessage
	}{
		{"wholly empty", agent.ChatMessage{}},
		{"whitespace only", agent.ChatMessage{Text: " \n\t "}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := s.buildPromptBlocks(tc.msg)
			assert.Error(t, err, "an empty prompt must not be delivered as a successful zero-byte turn")
			assert.Nil(t, blocks)
		})
	}
}

// A message with real text still builds exactly one text block, unchanged.
func TestBuildPromptBlocksKeepsTextPath(t *testing.T) {
	s := &chatSession{}
	blocks, err := s.buildPromptBlocks(agent.ChatMessage{Text: "hello"})
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	assert.Equal(t, "hello", blocks[0].Text.Text)
}

// U012-F06: deliverBlock's default branch flattened ANY unrecognized kind to
// TextBlock(b.Text) — for a block whose kind we do not understand that is an
// empty text block, a silent drop, exactly what the function's doc promises
// never to do. An unknown kind must render a visible placeholder naming it.
func TestDeliverBlockUnknownKindIsVisible(t *testing.T) {
	s := &chatSession{}
	raw, err := json.Marshal(map[string]any{"type": "hologram", "mimeType": "model/gltf+json", "data": "AAAA"})
	require.NoError(t, err)

	out := s.deliverBlock(agent.ContentBlock{Kind: "hologram", Raw: raw})
	require.NotNil(t, out.Text, "an unknown block kind must still deliver a text block")
	assert.NotEmpty(t, strings.TrimSpace(out.Text.Text), "an unknown block kind must not vanish into an empty text block")
	assert.Contains(t, out.Text.Text, "hologram", "the placeholder must name the kind that was not delivered")
}

// The plain "text" kind is untouched by the unknown-kind handling.
func TestDeliverBlockTextKindUnchanged(t *testing.T) {
	s := &chatSession{}
	out := s.deliverBlock(agent.ContentBlock{Kind: "text", Text: "plain"})
	require.NotNil(t, out.Text)
	assert.Equal(t, "plain", out.Text.Text)
}
