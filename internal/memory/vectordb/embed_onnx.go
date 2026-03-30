//go:build vectors && onnx

package vectordb

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/sugarme/tokenizer"
	ort "github.com/yalue/onnxruntime_go"

	"github.com/SophisticatedContextManager/scm/internal/memory/vectordb/onnxlib"
)

const (
	// Model dimensions for all-MiniLM-L6-v2
	defaultModelDim     = 384
	defaultMaxSeqLength = 256
)

var (
	onnxInitOnce sync.Once
	onnxInitErr  error
)

// initONNXRuntime initializes the ONNX runtime using the embedded library.
func initONNXRuntime() error {
	onnxInitOnce.Do(func() {
		// Extract embedded ONNX runtime if available
		if onnxlib.IsEmbedded() {
			libPath, err := onnxlib.Init()
			if err != nil {
				onnxInitErr = fmt.Errorf("extract embedded onnx runtime: %w", err)
				return
			}
			// Tell onnxruntime_go where to find the library
			ort.SetSharedLibraryPath(libPath)
		}
		// Initialize the environment
		onnxInitErr = ort.InitializeEnvironment()
	})
	return onnxInitErr
}

// ONNXEmbedder generates embeddings using ONNX runtime with sentence-transformers.
type ONNXEmbedder struct {
	session   *ort.DynamicAdvancedSession
	tokenizer *tokenizer.Tokenizer
	modelPath string
	dimension int
	maxSeqLen int
}

// ONNXEmbedderConfig configures the ONNX embedder.
type ONNXEmbedderConfig struct {
	ModelDir  string // Directory containing model.onnx and tokenizer.json
	Dimension int    // Embedding dimension (default: 384 for MiniLM)
	MaxSeqLen int    // Maximum sequence length (default: 256)
}

// NewONNXEmbedder creates a new ONNX-based embedder.
func NewONNXEmbedder(cfg ONNXEmbedderConfig) (*ONNXEmbedder, error) {
	if cfg.ModelDir == "" {
		return nil, fmt.Errorf("model directory required")
	}

	modelPath := filepath.Join(cfg.ModelDir, "model.onnx")
	tokenizerPath := filepath.Join(cfg.ModelDir, "tokenizer.json")

	// Check files exist
	if _, err := os.Stat(modelPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("model file not found: %s", modelPath)
	}
	if _, err := os.Stat(tokenizerPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("tokenizer file not found: %s", tokenizerPath)
	}

	// Initialize ONNX runtime (uses embedded library if available)
	if err := initONNXRuntime(); err != nil {
		return nil, fmt.Errorf("init onnx runtime: %w", err)
	}

	// Load tokenizer (pure Go - no CGO dependency)
	tk := tokenizer.NewTokenizerFromFile(tokenizerPath)
	if tk == nil {
		return nil, fmt.Errorf("failed to load tokenizer from %s", tokenizerPath)
	}

	dimension := cfg.Dimension
	if dimension == 0 {
		dimension = defaultModelDim
	}

	maxSeqLen := cfg.MaxSeqLen
	if maxSeqLen == 0 {
		maxSeqLen = defaultMaxSeqLength
	}

	// Create session options
	options, err := ort.NewSessionOptions()
	if err != nil {
		return nil, fmt.Errorf("create session options: %w", err)
	}
	defer options.Destroy()

	// Create dynamic session (allows variable-length inputs)
	// Input names for sentence transformers: input_ids, attention_mask, token_type_ids
	// Output name: last_hidden_state (or sentence_embedding for some models)
	inputNames := []string{"input_ids", "attention_mask", "token_type_ids"}
	outputNames := []string{"last_hidden_state"}

	session, err := ort.NewDynamicAdvancedSession(modelPath, inputNames, outputNames, options)
	if err != nil {
		return nil, fmt.Errorf("create onnx session: %w", err)
	}

	return &ONNXEmbedder{
		session:   session,
		tokenizer: tk,
		modelPath: modelPath,
		dimension: dimension,
		maxSeqLen: maxSeqLen,
	}, nil
}

// Embed generates an embedding for the given text.
func (e *ONNXEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	// Tokenize the text using pure Go tokenizer
	encoding, err := e.tokenizer.EncodeSingle(text, true)
	if err != nil {
		return nil, fmt.Errorf("tokenize text: %w", err)
	}
	ids := encoding.GetIds()

	// Truncate if necessary
	seqLen := len(ids)
	if seqLen > e.maxSeqLen {
		seqLen = e.maxSeqLen
		ids = ids[:seqLen]
	}

	// Create attention mask (all 1s for real tokens)
	// Create token type IDs (all 0s for single sequence)
	inputIDs := make([]int64, seqLen)
	attnMask := make([]int64, seqLen)
	tokenTypeIDs := make([]int64, seqLen)

	for i := 0; i < seqLen; i++ {
		inputIDs[i] = int64(ids[i])
		attnMask[i] = 1
		tokenTypeIDs[i] = 0
	}

	// Create input tensors (batch size 1)
	inputShape := ort.NewShape(1, int64(seqLen))

	inputIDsTensor, err := ort.NewTensor(inputShape, inputIDs)
	if err != nil {
		return nil, fmt.Errorf("create input_ids tensor: %w", err)
	}
	defer inputIDsTensor.Destroy()

	attnMaskTensor, err := ort.NewTensor(inputShape, attnMask)
	if err != nil {
		return nil, fmt.Errorf("create attention_mask tensor: %w", err)
	}
	defer attnMaskTensor.Destroy()

	typeIDsTensor, err := ort.NewTensor(inputShape, tokenTypeIDs)
	if err != nil {
		return nil, fmt.Errorf("create token_type_ids tensor: %w", err)
	}
	defer typeIDsTensor.Destroy()

	// Create output tensor
	outputShape := ort.NewShape(1, int64(seqLen), int64(e.dimension))
	outputTensor, err := ort.NewEmptyTensor[float32](outputShape)
	if err != nil {
		return nil, fmt.Errorf("create output tensor: %w", err)
	}
	defer outputTensor.Destroy()

	// Run inference with dynamic session
	inputs := []ort.Value{inputIDsTensor, attnMaskTensor, typeIDsTensor}
	outputs := []ort.Value{outputTensor}

	if err := e.session.Run(inputs, outputs); err != nil {
		return nil, fmt.Errorf("run inference: %w", err)
	}

	// Get output and perform mean pooling
	output := outputTensor.GetData()
	embedding := meanPool(output, seqLen, e.dimension)

	// Normalize
	normalizeFloat32(embedding)

	return embedding, nil
}

// Close releases resources.
func (e *ONNXEmbedder) Close() error {
	// tokenizer is pure Go - no cleanup needed
	if e.session != nil {
		e.session.Destroy()
	}
	// Cleanup embedded library temp files (no-op on Linux with memfd)
	onnxlib.Cleanup()
	return ort.DestroyEnvironment()
}

// meanPool performs mean pooling over token embeddings.
func meanPool(output []float32, seqLen, dimension int) []float32 {
	embedding := make([]float32, dimension)

	for d := 0; d < dimension; d++ {
		var sum float32
		for t := 0; t < seqLen; t++ {
			sum += output[t*dimension+d]
		}
		embedding[d] = sum / float32(seqLen)
	}

	return embedding
}

// normalizeFloat32 normalizes a vector to unit length.
func normalizeFloat32(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x * x)
	}

	if sum == 0 {
		return
	}

	magnitude := float32(math.Sqrt(sum))
	for i := range v {
		v[i] /= magnitude
	}
}
