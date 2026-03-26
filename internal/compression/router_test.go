package compression

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockCompressor implements the Compressor interface for testing
type MockCompressor struct {
	canHandleFunc func(ct ContentType) bool
	compressFunc  func(ctx context.Context, content string, ratio float64) (Result, error)
}

func (m *MockCompressor) CanHandle(ct ContentType) bool {
	if m.canHandleFunc != nil {
		return m.canHandleFunc(ct)
	}
	return false
}

func (m *MockCompressor) Compress(ctx context.Context, content string, ratio float64) (Result, error) {
	if m.compressFunc != nil {
		return m.compressFunc(ctx, content, ratio)
	}
	return Result{}, fmt.Errorf("not implemented")
}

// =============================================================================
// Router Construction Tests
// =============================================================================

// TestNewRouter verifies router initialization with default compressors
func TestNewRouter(t *testing.T) {
	router := NewRouter()

	assert.NotNil(t, router)
	assert.NotNil(t, router.compressors)
	assert.Len(t, router.compressors, 2) // Code and JSON compressors
	assert.Nil(t, router.FallbackCompressor)
}

// TestRouter_WithFallback verifies setting a fallback compressor
func TestRouter_WithFallback(t *testing.T) {
	router := NewRouter()
	fallback := &MockCompressor{}

	result := router.WithFallback(fallback)

	assert.Equal(t, router, result) // Should return self for chaining
	assert.Equal(t, fallback, router.FallbackCompressor)
}

// TestRouter_AddCompressor verifies adding custom compressors
func TestRouter_AddCompressor(t *testing.T) {
	router := NewRouter()
	initialCount := len(router.compressors)

	custom := &MockCompressor{}
	result := router.AddCompressor(custom)

	assert.Equal(t, router, result) // Should return self for chaining
	assert.Len(t, router.compressors, initialCount+1)
}

// TestRouter_Chaining verifies method chaining
func TestRouter_Chaining(t *testing.T) {
	fallback := &MockCompressor{}
	custom := &MockCompressor{}

	router := NewRouter().
		WithFallback(fallback).
		AddCompressor(custom)

	assert.Equal(t, fallback, router.FallbackCompressor)
	assert.Len(t, router.compressors, 3)
}

// =============================================================================
// Router Compression Tests
// =============================================================================

// TestRouter_Compress_WithSpecializedCompressor verifies routing to specific compressor
func TestRouter_Compress_WithSpecializedCompressor(t *testing.T) {
	router := NewRouter()

	// Compress Go code - should route to code compressor
	ctx := context.Background()
	result, err := router.Compress(ctx, "main.go", "package main\nfunc main() {}", 0.5)

	require.NoError(t, err)
	assert.NotNil(t, result)
	assert.NotEmpty(t, result.Content)
}

// TestRouter_Compress_WithFallback verifies fallback compression
func TestRouter_Compress_WithFallback(t *testing.T) {
	fallback := &MockCompressor{
		canHandleFunc: func(ct ContentType) bool { return true },
		compressFunc: func(ctx context.Context, content string, ratio float64) (Result, error) {
			return Result{
				Content:        content,
				OriginalSize:   len(content),
				CompressedSize: len(content) / 2,
				Ratio:          0.5,
				ModelID:        "fallback-model",
			}, nil
		},
	}

	router := NewRouter().WithFallback(fallback)

	ctx := context.Background()
	result, err := router.Compress(ctx, "unknown.txt", "Some text content", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "fallback-model", result.ModelID)
}

// TestRouter_Compress_NoHandler verifies graceful fallback when no compressor handles type
func TestRouter_Compress_NoHandler(t *testing.T) {
	router := NewRouter() // No fallback compressor

	ctx := context.Background()
	result, err := router.Compress(ctx, "unknown.xyz", "Some unknown content", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "Some unknown content", result.Content)
	assert.Equal(t, 1.0, result.Ratio) // No compression
	assert.Len(t, result.CompressedElements, 1)
	assert.Contains(t, result.CompressedElements[0], "no compressor")
}

// TestRouter_CompressWithType verifies explicit type routing
func TestRouter_CompressWithType(t *testing.T) {
	fallback := &MockCompressor{
		canHandleFunc: func(ct ContentType) bool { return ct == ContentTypeMarkdown },
		compressFunc: func(ctx context.Context, content string, ratio float64) (Result, error) {
			return Result{
				Content:        "# Summary\n" + content[:20],
				OriginalSize:   len(content),
				CompressedSize: 30,
				Ratio:          float64(30) / float64(len(content)),
				ModelID:        "markdown-compressor",
			}, nil
		},
	}

	router := NewRouter().WithFallback(fallback)

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeMarkdown, "Long markdown content here...", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "markdown-compressor", result.ModelID)
}

// TestRouter_CompressWithType_NoHandler verifies type not found
func TestRouter_CompressWithType_NoHandler(t *testing.T) {
	router := NewRouter() // No fallback

	ctx := context.Background()
	result, err := router.CompressWithType(ctx, ContentTypeMarkdown, "Some markdown", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "Some markdown", result.Content)
	assert.Equal(t, 1.0, result.Ratio)
}

// =============================================================================
// LLM Compressor Tests
// =============================================================================

// TestNewLLMCompressor verifies LLM compressor construction
func TestNewLLMCompressor(t *testing.T) {
	fn := func(ctx context.Context, content string) (string, string, error) {
		return content, "llm-model", nil
	}

	compressor := NewLLMCompressor(fn)

	assert.NotNil(t, compressor)
	assert.NotNil(t, compressor.CompressFunc)
}

// TestLLMCompressor_CanHandle verifies supported content types
func TestLLMCompressor_CanHandle(t *testing.T) {
	fn := func(ctx context.Context, content string) (string, string, error) {
		return content, "llm", nil
	}
	compressor := NewLLMCompressor(fn)

	tests := []struct {
		contentType ContentType
		expected    bool
	}{
		{ContentTypeMarkdown, true},
		{ContentTypeText, true},
		{ContentTypeUnknown, true},
		{ContentTypeYAML, true},
		{ContentTypeGo, false},
		{ContentTypeJSON, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.contentType), func(t *testing.T) {
			result := compressor.CanHandle(tt.contentType)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestLLMCompressor_Compress verifies compression with LLM function
func TestLLMCompressor_Compress(t *testing.T) {
	fn := func(ctx context.Context, content string) (string, string, error) {
		return "compressed content", "gpt-4", nil
	}
	compressor := NewLLMCompressor(fn)

	ctx := context.Background()
	result, err := compressor.Compress(ctx, "Original content that is longer", 0.5)

	require.NoError(t, err)
	assert.Equal(t, "compressed content", result.Content)
	assert.Equal(t, "gpt-4", result.ModelID)
	assert.Equal(t, 31, result.OriginalSize)
	assert.Equal(t, 18, result.CompressedSize)
	assert.Contains(t, result.PreservedElements, "llm-compressed")
}

// TestLLMCompressor_Compress_FunctionError verifies error propagation
func TestLLMCompressor_Compress_FunctionError(t *testing.T) {
	fn := func(ctx context.Context, content string) (string, string, error) {
		return "", "", fmt.Errorf("compression failed")
	}
	compressor := NewLLMCompressor(fn)

	ctx := context.Background()
	_, err := compressor.Compress(ctx, "Content", 0.5)

	require.Error(t, err)
	assert.Equal(t, "compression failed", err.Error())
}

// TestLLMCompressor_Compress_NoFunction verifies missing function handling
func TestLLMCompressor_Compress_NoFunction(t *testing.T) {
	compressor := NewLLMCompressor(nil)

	ctx := context.Background()
	result, err := compressor.Compress(ctx, "Content", 0.5)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configured")
	assert.Equal(t, "Content", result.Content)
	assert.Equal(t, 1.0, result.Ratio)
}

// TestLLMCompressor_Ratio verifies correct ratio calculation
func TestLLMCompressor_Ratio(t *testing.T) {
	fn := func(ctx context.Context, content string) (string, string, error) {
		// Return exactly half the size
		return content[:len(content)/2], "model", nil
	}
	compressor := NewLLMCompressor(fn)

	ctx := context.Background()
	result, err := compressor.Compress(ctx, "1234567890", 0.5)

	require.NoError(t, err)
	assert.Equal(t, 10, result.OriginalSize)
	assert.Equal(t, 5, result.CompressedSize)
	assert.InDelta(t, 0.5, result.Ratio, 0.01)
}
