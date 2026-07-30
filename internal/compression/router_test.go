package compression

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Router Construction Tests
// =============================================================================

// TestNewRouter verifies router initialization with default compressors
func TestNewRouter(t *testing.T) {
	router := NewRouter()

	assert.NotNil(t, router)
	assert.NotNil(t, router.compressors)
	assert.Len(t, router.compressors, 2) // Code and JSON compressors
}

// =============================================================================
// Router CompressWithType Tests
// =============================================================================

// TestRouter_CompressWithType_NoHandler verifies type not found
func TestRouter_CompressWithType_NoHandler(t *testing.T) {
	router := NewRouter()

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeMarkdown, "Some markdown")

	require.NoError(t, err)
	assert.Equal(t, "Some markdown", result.Content)
	assert.Equal(t, 1.0, result.Ratio)
}

// TestRouter_CompressWithType_Code verifies code compression routing
func TestRouter_CompressWithType_Code(t *testing.T) {
	router := NewRouter()

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeGo, "package main\nfunc main() {}")

	require.NoError(t, err)
	assert.NotEmpty(t, result.Content)
}

// TestRouter_CompressWithType_JSON verifies JSON compression routing
func TestRouter_CompressWithType_JSON(t *testing.T) {
	router := NewRouter()

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeJSON, `{"key": "value"}`)

	require.NoError(t, err)
	assert.NotEmpty(t, result.Content)
}

// TestRouter_NoHandlerMatchesVerbatimResult is the parity gate over the two
// verbatim pass-throughs in this package: the Router's own no-compressor
// fallback and verbatimResult, the helper every compressor degrades through.
// A pass-through that diverges from the canonical one is a Result the caller
// cannot classify — and Ratio in particular is load-bearing, because the only
// production caller (cli.distillWithModel) treats `Ratio < 0.7` as "structural
// compression succeeded, use it" and anything else as "fall back to the LLM".
// A fallback claiming a compressed ratio for unchanged content would ship the
// original bytes as a distillation.
func TestRouter_NoHandlerMatchesVerbatimResult(t *testing.T) {
	const content = "key: value\n"
	got, err := NewRouter().CompressWithType(context.Background(), ContentTypeYAML, content)
	require.NoError(t, err)
	assert.Equal(t, verbatimResult(content, ""), got,
		"the no-compressor fallback must be the canonical verbatim Result, field for field")
	assert.Equal(t, 1.0, got.Ratio, "unchanged content must never claim a compressed ratio")
}
