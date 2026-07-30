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
func (r *Router) CompressWithType(ctx context.Context, contentType ContentType, content string) (Result, error) {
	for _, c := range r.compressors {
		if c.CanHandle(contentType) {
			return c.Compress(ctx, contentType, content)
		}
	}

	// No compressor claims this type: degrade through the package's canonical
	// verbatim pass-through, untagged — no model or grammar touched this
	// content, so there is nothing to name.
	return verbatimResult(content, ""), nil
}
