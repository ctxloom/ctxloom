//go:build memory

package vectordb

import (
	"context"
	"time"
)

// MemoryChunk represents a chunk of session memory stored in the vector database.
type MemoryChunk struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	ChunkIndex    int       `json:"chunk_index"`
	Timestamp     time.Time `json:"timestamp"`
	Content       string    `json:"content"`       // Distilled content
	Topics        []string  `json:"topics"`        // Extracted topics
	ToolsUsed     []string  `json:"tools_used"`    // Tools mentioned
	FilesModified []string  `json:"files_modified"` // Files mentioned
}

// QueryOpts configures a memory query.
type QueryOpts struct {
	Limit     int     // Max results (default 5)
	TimeRange string  // today, week, month, all
	SessionID string  // Filter to specific session
	Threshold float32 // Similarity threshold (default 0.7)
}

// QueryResult represents a single query result.
type QueryResult struct {
	Chunk      MemoryChunk `json:"chunk"`
	Similarity float32     `json:"similarity"`
}

// Store defines the interface for vector storage operations.
type Store interface {
	// Add stores a memory chunk with its embedding.
	Add(ctx context.Context, chunk MemoryChunk) error

	// Query searches for similar chunks.
	Query(ctx context.Context, query string, opts QueryOpts) ([]QueryResult, error)

	// Delete removes chunks by session ID.
	DeleteSession(ctx context.Context, sessionID string) error

	// Close closes the store.
	Close() error
}

// Embedder defines the interface for generating embeddings.
type Embedder interface {
	// Embed generates an embedding vector for the given text.
	Embed(ctx context.Context, text string) ([]float32, error)

	// Close releases any resources.
	Close() error
}

// DefaultQueryOpts returns sensible default query options.
func DefaultQueryOpts() QueryOpts {
	return QueryOpts{
		Limit:     5,
		TimeRange: "week",
		Threshold: 0.7,
	}
}
