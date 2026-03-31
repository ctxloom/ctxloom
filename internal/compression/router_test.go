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
	result, err := router.CompressWithType(ctx, ContentTypeMarkdown, "Some markdown", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "Some markdown", result.Content)
	assert.Equal(t, 1.0, result.Ratio)
}

// TestRouter_CompressWithType_Code verifies code compression routing
func TestRouter_CompressWithType_Code(t *testing.T) {
	router := NewRouter()

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeGo, "package main\nfunc main() {}", 0.5)

	require.NoError(t, err)
	assert.NotEmpty(t, result.Content)
}

// TestRouter_CompressWithType_JSON verifies JSON compression routing
func TestRouter_CompressWithType_JSON(t *testing.T) {
	router := NewRouter()

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeJSON, `{"key": "value"}`, 0.5)

	require.NoError(t, err)
	assert.NotEmpty(t, result.Content)
}
