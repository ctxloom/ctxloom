//go:build memory && vectors

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/memory/vectordb"
)

// indexSessionToVectorDB indexes a compacted session into the vector database.
// This enables semantic search across past sessions.
func indexSessionToVectorDB(cfg *config.Config, sessionID string) error {
	// Check if vectors are enabled
	if !cfg.Memory.Vectors.Enabled {
		return nil
	}

	memoryDir := getMemoryDir(cfg)

	// Load distilled session
	distilled, err := vectordb.LoadDistilledSession(memoryDir, sessionID)
	if err != nil {
		return fmt.Errorf("load distilled session: %w", err)
	}

	// Initialize vector store
	store, embedderType, err := initVectorStoreForMemory(memoryDir, cfg)
	if err != nil {
		return fmt.Errorf("initialize vector store: %w", err)
	}
	defer store.Close()

	// Create chunk from distilled content
	chunk := vectordb.MemoryChunk{
		ID:         fmt.Sprintf("%s-0", sessionID),
		SessionID:  sessionID,
		ChunkIndex: 0,
		Timestamp:  distilled.CreatedAt,
		Content:    distilled.Content,
	}

	// Add to vector store
	ctx := context.Background()
	if err := store.Add(ctx, chunk); err != nil {
		return fmt.Errorf("index chunk: %w", err)
	}

	fmt.Printf("Indexed to vector DB (embedder: %s)\n", embedderType)
	return nil
}

// initVectorStoreForMemory initializes the vector store with the best available embedder.
func initVectorStoreForMemory(memoryDir string, cfg *config.Config) (*vectordb.ChromemStore, vectordb.EmbedderType, error) {
	// Create embedder config
	embedderCfg := vectordb.EmbedderConfig{
		ONNXModelDir: vectordb.GetDefaultModelDir(),
	}

	// Override model path if configured
	if cfg.Memory.Vectors.ModelPath != "" {
		embedderCfg.ONNXModelDir = filepath.Dir(cfg.Memory.Vectors.ModelPath)
	}

	result, err := vectordb.NewEmbedder(embedderCfg)
	if err != nil {
		// Log but continue - we might have fallen back to simple embedder
		fmt.Fprintf(os.Stderr, "warning: embedder init: %v (using fallback)\n", err)
	}

	// Create store
	store, err := vectordb.NewChromemStore(memoryDir, result.Embedder)
	if err != nil {
		if result.Embedder != nil {
			result.Embedder.Close()
		}
		return nil, "", fmt.Errorf("create store: %w", err)
	}

	return store, result.Type, nil
}
