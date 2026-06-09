//go:build treesitter

package compression

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The routed content type must pick the grammar. Rust without "->" misses the
// content heuristics; re-sniffing used to default it to the GO grammar, which
// finds no Go nodes and silently compresses the file to near-empty.
func TestCodeCompressor_HonorsRoutedContentType(t *testing.T) {
	c := NewCodeCompressor()
	source := "fn main() {\n    let body_token = 1;\n    println!(\"{}\", body_token);\n}\n"

	result, err := c.Compress(context.Background(), ContentTypeRust, source, 0.5)
	require.NoError(t, err)
	assert.Equal(t, "ast:rust", result.ModelID, "the routed type, not a re-sniff, must pick the grammar")
	assert.Contains(t, result.Content, "fn main", "the signature must survive")
}

// Unsniffable content with an unknown type degrades to verbatim — never to a
// guessed grammar that would silently lose the content.
func TestCodeCompressor_UnknownContentDegradesVerbatim(t *testing.T) {
	c := NewCodeCompressor()
	source := "just some plain prose, no code here at all\n"

	result, err := c.Compress(context.Background(), ContentTypeUnknown, source, 0.5)
	require.NoError(t, err)
	assert.Equal(t, source, result.Content, "unparseable content must pass through verbatim")
	assert.Equal(t, 1.0, result.Ratio)
}
