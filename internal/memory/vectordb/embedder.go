//go:build vectors

package vectordb

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// defaultModelName is the default ONNX model for embeddings.
	defaultModelName = "all-MiniLM-L6-v2"
)

// EmbedderType indicates which embedder is being used.
type EmbedderType string

const (
	EmbedderTypeONNX   EmbedderType = "onnx"
	EmbedderTypeSimple EmbedderType = "simple"
)

// EmbedderConfig configures embedder selection.
type EmbedderConfig struct {
	// PreferredType specifies the preferred embedder type.
	// If empty, auto-selects based on availability.
	PreferredType EmbedderType

	// ONNXModelDir is the directory containing ONNX model files.
	// Required for ONNX embedder.
	ONNXModelDir string
}

// EmbedderResult contains the created embedder and its type.
type EmbedderResult struct {
	Embedder Embedder
	Type     EmbedderType
}

// NewEmbedder creates an embedder based on configuration and availability.
// It tries embedders in order: ONNX -> Simple
func NewEmbedder(cfg EmbedderConfig) (*EmbedderResult, error) {
	// If a specific type is requested, try only that
	if cfg.PreferredType != "" {
		return newSpecificEmbedder(cfg)
	}

	// Auto-select: try in order of quality
	var lastErr error

	// 1. Try ONNX if model directory is specified
	if cfg.ONNXModelDir != "" {
		embedder, err := tryONNX(cfg)
		if err == nil {
			return &EmbedderResult{Embedder: embedder, Type: EmbedderTypeONNX}, nil
		}
		lastErr = err
	}

	// 2. Fall back to simple embedder
	return &EmbedderResult{
		Embedder: NewSimpleEmbedder(),
		Type:     EmbedderTypeSimple,
	}, lastErr // Return last error for logging, but still provide simple embedder
}

func newSpecificEmbedder(cfg EmbedderConfig) (*EmbedderResult, error) {
	switch cfg.PreferredType {
	case EmbedderTypeONNX:
		embedder, err := tryONNX(cfg)
		if err != nil {
			return nil, fmt.Errorf("onnx embedder: %w", err)
		}
		return &EmbedderResult{Embedder: embedder, Type: EmbedderTypeONNX}, nil

	case EmbedderTypeSimple:
		return &EmbedderResult{Embedder: NewSimpleEmbedder(), Type: EmbedderTypeSimple}, nil

	default:
		return nil, fmt.Errorf("unknown embedder type: %s", cfg.PreferredType)
	}
}

func tryONNX(cfg EmbedderConfig) (Embedder, error) {
	if cfg.ONNXModelDir == "" {
		return nil, fmt.Errorf("ONNX model directory not specified")
	}

	// Check if model files exist
	modelPath := filepath.Join(cfg.ONNXModelDir, "model.onnx")
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("model file not found: %s", modelPath)
	}

	// NewONNXEmbedder returns error if not built with onnx tag
	embedder, err := NewONNXEmbedder(ONNXEmbedderConfig{
		ModelDir: cfg.ONNXModelDir,
	})
	if err != nil {
		return nil, err
	}

	return embedder, nil
}

// GetDefaultModelDir returns the default directory for ONNX models.
func GetDefaultModelDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".scm", "models", defaultModelName)
}
