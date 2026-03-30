//go:build vectors && !onnx

package vectordb

import (
	"context"
	"fmt"
)

// ONNXEmbedderConfig configures the ONNX embedder.
type ONNXEmbedderConfig struct {
	ModelDir  string
	Dimension int
	MaxSeqLen int
}

// ONNXEmbedder is a stub when onnx tag is not set.
type ONNXEmbedder struct{}

// NewONNXEmbedder returns an error when ONNX is not available.
func NewONNXEmbedder(_ ONNXEmbedderConfig) (*ONNXEmbedder, error) {
	return nil, fmt.Errorf("ONNX embedder not available: build with -tags 'memory,vectors,onnx' to enable")
}

// Embed is a stub.
func (e *ONNXEmbedder) Embed(_ context.Context, _ string) ([]float32, error) {
	return nil, fmt.Errorf("ONNX embedder not available")
}

// Close is a stub.
func (e *ONNXEmbedder) Close() error {
	return nil
}
