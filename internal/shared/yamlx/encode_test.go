package yamlx

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// TestMarshal_TwoSpace pins the one encoding: a document renders in full at
// two-space indentation, nothing left unflushed when the bytes are taken.
func TestMarshal_TwoSpace(t *testing.T) {
	const doc = "title: a plan\nsessions:\n  - one\n  - two\nnested:\n  key:\n    deeper: x # kept\n"
	var root yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(doc), &root))
	out, err := Marshal(&root)
	require.NoError(t, err)
	require.Equal(t, doc, string(out))

	out, err = Marshal(map[string]any{"a": map[string]any{"b": []string{"c"}}})
	require.NoError(t, err)
	require.Equal(t, "a:\n  b:\n    - c\n", string(out))
}

// TestMarshal_PartialDocumentIsNeverReturned: callers hand the bytes to an
// atomic write over a user's file, so a rendering that did not complete must
// be an error and NO bytes, never what the encoder happened to buffer. The
// node below (a child with no kind) is one the emitter refuses mid-stream,
// leaving it in a state where the stream close fails too.
func TestMarshal_PartialDocumentIsNeverReturned(t *testing.T) {
	broken := &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Value: "no-kind"}}}
	out, err := Marshal(broken)
	require.Error(t, err)
	require.Nil(t, out)
}
