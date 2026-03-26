//go:build !treesitter

package compression

import (
	"context"
)

// CodeCompressor is a stub when tree-sitter is not available.
// It does not handle any content types.
type CodeCompressor struct{}

// NewCodeCompressor creates a no-op code compressor.
func NewCodeCompressor() *CodeCompressor {
	return &CodeCompressor{}
}

// CanHandle always returns false when tree-sitter is not available.
func (c *CodeCompressor) CanHandle(ct ContentType) bool {
	return false
}

// Compress returns the original content unchanged.
func (c *CodeCompressor) Compress(ctx context.Context, content string, ratio float64) (Result, error) {
	return Result{
		Content:        content,
		OriginalSize:   len(content),
		CompressedSize: len(content),
		Ratio:          1.0,
		CompressedElements: []string{
			"tree-sitter not available (build with -tags treesitter)",
		},
	}, nil
}
