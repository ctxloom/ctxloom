package compression

import (
	"context"
	"errors"
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

// failingCompressor claims a type and fails on it — the shape of any compressor
// whose work can genuinely fail (the LLM-backed one this package's Result.ModelID
// doc already anticipates: "ast:go", "claude-3-sonnet").
type failingCompressor struct{ err error }

func (f failingCompressor) CanHandle(ct ContentType) bool { return ct == ContentTypeYAML }
func (f failingCompressor) Compress(context.Context, ContentType, string) (Result, error) {
	return Result{}, f.err
}

// Compress's error return is structurally nil in all three current
// implementations, which might suggest retiring it. The channel is LIVE,
// not dead: the router propagates it unchanged, and the sole production caller
// (cli.distillWithModel) gates on `err == nil && result.Ratio < 0.7` — a
// non-nil error is exactly how a compressor asks to fall back to the LLM. This
// pins that path so removing the error return has to fail here first.
func TestRouter_CompressWithType_PropagatesCompressorError(t *testing.T) {
	sentinel := errors.New("compressor refused this content")
	r := &Router{compressors: []Compressor{failingCompressor{err: sentinel}}}

	_, err := r.CompressWithType(context.Background(), ContentTypeYAML, "key: value\n")
	assert.ErrorIs(t, err, sentinel, "the router hands a compressor's failure straight to the caller")
}

// The two errors the tree-sitter compressor CREATES are deliberately degraded
// to verbatim rather than returned — fault tolerance, so unparseable content
// still reaches the model. One of them is unreachable through the router at
// all: CanHandle gates Compress, and every type it admits has a grammar, so
// parserPool's "unsupported language" can only be produced by calling Compress
// directly. This pins the degrade, so a future change that returns those errors
// instead is a deliberate one.
func TestRouter_UnsupportedTypeNeverReachesACompressor(t *testing.T) {
	r := NewRouter()
	for _, ct := range []ContentType{ContentTypeYAML, ContentTypeMarkdown, ContentTypeUnknown} {
		result, err := r.CompressWithType(context.Background(), ct, "content")
		require.NoError(t, err, "%s: no compressor claims it, so nothing can fail", ct)
		assert.Equal(t, "content", result.Content)
	}
}
