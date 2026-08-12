package spool

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// statDir reports whether path is a directory.
func statDir(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return st.IsDir(), nil
}

func TestParse_ReadsFrontmatterAndBody(t *testing.T) {
	raw := []byte("---\n" +
		"v: 1\n" +
		"id: 00000000000000000001.00000001.coord\n" +
		"kind: steer\n" +
		"from_harp: coord\n" +
		"to: ugly-icy-squid\n" +
		"in_reply_to: 00000000000000000000.00000009.agent\n" +
		"created: 2026-08-12T10:11:12.5Z\n" +
		"ttl_s: 60\n" +
		"structured:\n  decision: approve\n" +
		"---\n" +
		"Focus on the failing test.\n")

	msg, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, 1, msg.V)
	require.Equal(t, "00000000000000000001.00000001.coord", msg.ID)
	require.Equal(t, "steer", msg.Kind)
	require.Equal(t, "coord", msg.FromHarp)
	require.Equal(t, "ugly-icy-squid", msg.To)
	require.Equal(t, "00000000000000000000.00000009.agent", msg.InReplyTo)
	require.Equal(t, 60, msg.TTLSeconds)
	require.Equal(t, map[string]any{"decision": "approve"}, msg.Structured)
	require.Equal(t, "Focus on the failing test.\n", msg.Body)
	require.True(t, msg.Created.Equal(time.Date(2026, 8, 12, 10, 11, 12, 500000000, time.UTC)),
		"created must decode as RFC3339, got %v", msg.Created)
}

// TestParse_EmptyBodyIsLegal: frontmatter is the control plane, so a control
// message needs no body at all. A parser that required one would refuse every
// approval decision and every ack.
func TestParse_EmptyBodyIsLegal(t *testing.T) {
	raw := []byte("---\nkind: approval_decision\ncreated: 2026-08-12T10:11:12Z\n---\n")
	msg, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, "approval_decision", msg.Kind)
	require.Empty(t, msg.Body)

	out, err := msg.Encode()
	require.NoError(t, err)
	require.NotEmpty(t, out)
	back, err := Parse(out)
	require.NoError(t, err)
	require.Equal(t, "approval_decision", back.Kind)
	require.Empty(t, back.Body)
}

// TestEncode_PreservesUnknownFrontmatterKeys is the forward-compatibility
// ruling as a test: an older build rewriting a newer peer's file must carry
// the keys it does not understand THROUGH, not strip them. Stripping is the
// shorter implementation (marshal a struct) and is silent — the file still
// parses, still routes, and the peer's field is simply gone.
func TestEncode_PreservesUnknownFrontmatterKeys(t *testing.T) {
	raw := []byte("---\n" +
		"kind: message\n" +
		"created: 2026-08-12T10:11:12Z\n" +
		"future_scalar: keep-me\n" +
		"future_map:\n  nested: 7\n" +
		"future_list:\n  - a\n  - b\n" +
		"---\n" +
		"body text\n")

	msg, err := Parse(raw)
	require.NoError(t, err)
	require.ElementsMatch(t, []string{"future_scalar", "future_map", "future_list"}, msg.UnknownKeys(),
		"the parser must be able to name what it is carrying blind")

	// A rewrite that changes a known field must not disturb the unknown ones.
	msg.InReplyTo = "00000000000000000000.00000001.coord"
	out, err := msg.Encode()
	require.NoError(t, err)
	require.NotEmpty(t, out, "empty-source guard: an empty encode would satisfy every substring check below")

	text := string(out)
	require.Contains(t, text, "future_scalar: keep-me")
	require.Contains(t, text, "nested: 7")
	require.Contains(t, text, "- a")
	require.Contains(t, text, "- b")

	back, err := Parse(out)
	require.NoError(t, err)
	require.Equal(t, "message", back.Kind)
	require.Equal(t, "00000000000000000000.00000001.coord", back.InReplyTo)
	require.Equal(t, "body text\n", back.Body)
	require.ElementsMatch(t, []string{"future_scalar", "future_map", "future_list"}, back.UnknownKeys(),
		"unknown keys must survive a full parse->encode->parse round trip")
}

// TestEncode_PreservesKeyOrderAndComments: the unknown keys survive IN PLACE,
// with their comments, because the encoder patches the parsed node tree.
func TestEncode_PreservesKeyOrderAndComments(t *testing.T) {
	raw := []byte("---\n" +
		"# why this message exists\n" +
		"kind: message\n" +
		"future_scalar: keep-me\n" +
		"created: 2026-08-12T10:11:12Z\n" +
		"---\n" +
		"hello\n")
	msg, err := Parse(raw)
	require.NoError(t, err)
	out, err := msg.Encode()
	require.NoError(t, err)
	text := string(out)
	require.Contains(t, text, "# why this message exists")
	require.Less(t, strings.Index(text, "future_scalar"), strings.Index(text, "created"),
		"key order must be preserved, not re-sorted")
}

func TestParse_RefusesMalformed(t *testing.T) {
	cases := map[string]string{
		"no frontmatter at all":  "just a body\n",
		"unterminated fence":     "---\nkind: message\ncreated: 2026-08-12T10:11:12Z\n",
		"frontmatter not a map":  "---\n- one\n- two\n---\nbody\n",
		"broken yaml":            "---\nkind: [unclosed\n---\nbody\n",
		"missing kind":           "---\ncreated: 2026-08-12T10:11:12Z\n---\nbody\n",
		"missing created":        "---\nkind: message\n---\nbody\n",
		"non-RFC3339 created":    "---\nkind: message\ncreated: yesterday\n---\nbody\n",
		"kind wrong scalar type": "---\nkind:\n  a: b\ncreated: 2026-08-12T10:11:12Z\n---\nbody\n",
	}
	for what, raw := range cases {
		t.Run(what, func(t *testing.T) {
			msg, err := Parse([]byte(raw))
			require.Error(t, err, "Parse must refuse %s", what)
			require.Nil(t, msg)
		})
	}
}

// TestParse_BodyFenceIsNotEatenByTheHead pins the corruption this repo has
// been bitten by before: only the FIRST closing fence terminates the head.
func TestParse_BodyFenceIsNotEatenByTheHead(t *testing.T) {
	body := "intro\n\n---\n\nsection two with {{ braces }}\n"
	raw := []byte("---\nkind: message\ncreated: 2026-08-12T10:11:12Z\n---\n" + body)
	msg, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, body, msg.Body)

	out, err := msg.Encode()
	require.NoError(t, err)
	back, err := Parse(out)
	require.NoError(t, err)
	require.Equal(t, body, back.Body, "a body containing its own fence must survive a round trip")
}

func TestParse_AcceptsFutureVersion(t *testing.T) {
	raw := []byte("---\nv: 99\nkind: message\ncreated: 2026-08-12T10:11:12Z\n---\nhi\n")
	msg, err := Parse(raw)
	require.NoError(t, err)
	require.Equal(t, 99, msg.V, "a newer version must be carried, not refused: unknown keys already round-trip")
}

func TestEncode_OmitsEmptyOptionalFields(t *testing.T) {
	msg := &Message{Kind: "message", Created: time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC), Body: "hi\n"}
	out, err := msg.Encode()
	require.NoError(t, err)
	text := string(out)
	require.Contains(t, text, "kind: message")
	require.Contains(t, text, "v: 1")
	require.NotContains(t, text, "in_reply_to")
	require.NotContains(t, text, "ttl_s")
	require.NotContains(t, text, "structured")
	require.NotContains(t, text, "from_harp")
}
