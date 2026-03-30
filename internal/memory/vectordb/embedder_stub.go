//go:build !vectors

package vectordb

import "fmt"

// EmbedderType indicates which embedder is being used.
type EmbedderType string

const (
	EmbedderTypeONNX   EmbedderType = "onnx"
	EmbedderTypeSimple EmbedderType = "simple"
)

// EmbedderConfig configures embedder selection.
type EmbedderConfig struct {
	PreferredType EmbedderType
	ONNXModelDir  string
}

// EmbedderResult contains the created embedder and its type.
type EmbedderResult struct {
	Embedder Embedder
	Type     EmbedderType
}

// NewEmbedder is a stub when vectors feature is disabled.
func NewEmbedder(_ EmbedderConfig) (*EmbedderResult, error) {
	return nil, fmt.Errorf("vector embeddings not available: build with -tags 'memory,vectors' to enable")
}

// GetDefaultModelDir returns empty string when vectors is disabled.
func GetDefaultModelDir() string {
	return ""
}
