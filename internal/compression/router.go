package compression

import (
	"context"
)

// Router dispatches content to the appropriate compressor based on content type.
// For code and JSON, it uses fast local compression.
type Router struct {
	// Compressors registered by content type capability.
	compressors []Compressor
}

// NewRouter creates a router with default compressors.
func NewRouter() *Router {
	return &Router{
		compressors: []Compressor{
			NewCodeCompressor(),
			NewJSONCompressor(),
		},
	}
}

// CompressWithType compresses content with an explicit content type.
func (r *Router) CompressWithType(ctx context.Context, contentType ContentType, content string, ratio float64) (Result, error) {
	for _, c := range r.compressors {
		if c.CanHandle(contentType) {
			return c.Compress(ctx, content, ratio)
		}
	}

	// No compressor available - return original content
	return Result{
		Content:        content,
		OriginalSize:   len(content),
		CompressedSize: len(content),
		Ratio:          1.0,
	}, nil
}
