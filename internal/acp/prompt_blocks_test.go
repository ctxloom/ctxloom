package acp

import (
	"encoding/json"
	"fmt"
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

// U012-F13: mediaBlockDetail's "N bytes" is read by the MODEL and by a human
// as the size of the media that did not get delivered. It must be the size of
// the DECODED payload, not the length of its base64 transport encoding —
// base64 inflates by 4/3, so reporting the character count overstates every
// dropped image and audio block by about a third.
func TestMediaBlockDetailReportsDecodedByteCount(t *testing.T) {
	for _, tc := range []struct {
		name    string
		data    string // base64 payload as it rides the wire
		want    int    // real decoded byte count
		wantEnc int    // the base64 character count this must NOT report
	}{
		{"exact multiple of three", "AAAA", 3, 4},
		{"one padding byte", "AAAAAA==", 4, 8},
		{"two padding bytes", "AAAAAAA=", 5, 8},
		{"unpadded", "AAAAAAA", 5, 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"type": "image", "mimeType": "image/png", "data": tc.data})
			require.NoError(t, err)

			detail := mediaBlockDetail(raw)
			assert.Contains(t, detail, fmt.Sprintf("%d bytes", tc.want),
				"the detail must name the decoded payload size")
			assert.NotContains(t, detail, fmt.Sprintf("%d bytes", tc.wantEnc),
				"the detail must not name the base64 character count as a byte count")
		})
	}
}

// A block whose Raw carries no mimeType still yields no detail at all — the
// decoded-size correction must not turn "" into a bogus "0 bytes".
func TestMediaBlockDetailStaysEmptyWithoutMimeType(t *testing.T) {
	raw, err := json.Marshal(map[string]any{"type": "resource", "resource": map[string]any{"uri": "file:///x"}})
	require.NoError(t, err)
	assert.Empty(t, mediaBlockDetail(raw))
}

// The plain "text" kind is untouched by the unknown-kind handling.
func TestDeliverBlockTextKindUnchanged(t *testing.T) {
	s := &chatSession{}
	out := s.deliverBlock(agent.ContentBlock{Kind: "text", Text: "plain"})
	require.NotNil(t, out.Text)
	assert.Equal(t, "plain", out.Text.Text)
}
