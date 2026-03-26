//go:build memory && vectors

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/SophisticatedContextManager/scm/internal/memory/vectordb"
)

// getVectorTools returns MCP tool definitions for vector search features.
func getVectorTools() []mcpToolInfo {
	return []mcpToolInfo{
		{
			Name:        "query_memory",
			Description: "Search past session memory for relevant context using semantic similarity. Use this to find related information from previous sessions.",
			InputSchema: map[string]interface{}{
				"type":     "object",
				"required": []string{"query"},
				"properties": map[string]interface{}{
					"query": map[string]interface{}{
						"type":        "string",
						"description": "The search query (semantic similarity search)",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"description": "Maximum number of results (default: 5)",
					},
					"time_range": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"today", "week", "month", "all"},
						"description": "Filter by time range (default: week)",
					},
					"session_id": map[string]interface{}{
						"type":        "string",
						"description": "Filter to a specific session (optional)",
					},
					"threshold": map[string]interface{}{
						"type":        "number",
						"description": "Minimum similarity threshold 0.0-1.0 (default: 0.7)",
					},
				},
			},
		},
		{
			Name:        "index_session",
			Description: "Index a compacted session into the vector database for semantic search",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"session_id": map[string]interface{}{
						"type":        "string",
						"description": "Session ID to index (optional, defaults to most recent compacted)",
					},
				},
			},
		},
	}
}

// toolQueryMemory performs semantic search over stored memory chunks.
func (s *mcpServer) toolQueryMemory(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		Query     string  `json:"query"`
		Limit     int     `json:"limit"`
		TimeRange string  `json:"time_range"`
		SessionID string  `json:"session_id"`
		Threshold float32 `json:"threshold"`
	}
	_ = json.Unmarshal(args, &params)

	if params.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	// Initialize vector store
	memoryDir := filepath.Join(s.cfg.SCMDir, "memory")
	store, embedderType, err := initVectorStore(memoryDir, s.cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize vector store: %w", err)
	}
	defer store.Close()

	// Build query options
	opts := vectordb.DefaultQueryOpts()
	if params.Limit > 0 {
		opts.Limit = params.Limit
	}
	if params.TimeRange != "" {
		opts.TimeRange = params.TimeRange
	}
	if params.SessionID != "" {
		opts.SessionID = params.SessionID
	}
	if params.Threshold > 0 {
		opts.Threshold = params.Threshold
	}

	// Execute query
	results, err := store.Query(ctx, params.Query, opts)
	if err != nil {
		return nil, fmt.Errorf("query failed: %w", err)
	}

	// Format results
	type resultItem struct {
		SessionID  string   `json:"session_id"`
		ChunkIndex int      `json:"chunk_index"`
		Similarity float32  `json:"similarity"`
		Content    string   `json:"content"`
		Topics     []string `json:"topics,omitempty"`
		Timestamp  string   `json:"timestamp,omitempty"`
	}

	var items []resultItem
	for _, r := range results {
		item := resultItem{
			SessionID:  r.Chunk.SessionID,
			ChunkIndex: r.Chunk.ChunkIndex,
			Similarity: r.Similarity,
			Content:    r.Chunk.Content,
			Topics:     r.Chunk.Topics,
		}
		if !r.Chunk.Timestamp.IsZero() {
			item.Timestamp = r.Chunk.Timestamp.Format("2006-01-02 15:04")
		}
		items = append(items, item)
	}

	return map[string]interface{}{
		"results":       items,
		"count":         len(items),
		"embedder_type": string(embedderType),
	}, nil
}

// toolIndexSession indexes a compacted session into the vector database.
func (s *mcpServer) toolIndexSession(ctx context.Context, args json.RawMessage) (interface{}, error) {
	var params struct {
		SessionID string `json:"session_id"`
	}
	_ = json.Unmarshal(args, &params)

	memoryDir := filepath.Join(s.cfg.SCMDir, "memory")

	// Find session to index
	sessionID := params.SessionID
	if sessionID == "" {
		// Get most recent compacted session
		sessions, err := vectordb.ListDistilledSessions(memoryDir)
		if err != nil || len(sessions) == 0 {
			return nil, fmt.Errorf("no compacted sessions found")
		}
		sessionID = sessions[len(sessions)-1]
	}

	// Load distilled session
	distilled, err := vectordb.LoadDistilledSession(memoryDir, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load distilled session: %w", err)
	}

	// Initialize vector store
	store, embedderType, err := initVectorStore(memoryDir, s.cfg)
	if err != nil {
		return nil, fmt.Errorf("initialize vector store: %w", err)
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
	if err := store.Add(ctx, chunk); err != nil {
		return nil, fmt.Errorf("index chunk: %w", err)
	}

	return map[string]interface{}{
		"session_id":    sessionID,
		"indexed":       true,
		"content_chars": len(distilled.Content),
		"embedder_type": string(embedderType),
	}, nil
}

// initVectorStore initializes the vector store with the best available embedder.
func initVectorStore(memoryDir string, cfg interface{}) (*vectordb.ChromemStore, vectordb.EmbedderType, error) {
	// Create embedder
	embedderCfg := vectordb.EmbedderConfig{
		ONNXModelDir: vectordb.GetDefaultModelDir(),
	}

	result, err := vectordb.NewEmbedder(embedderCfg)
	if err != nil {
		// Log but continue - we might have fallen back to simple embedder
		fmt.Fprintf(os.Stderr, "SCM: warning: embedder init: %v (using fallback)\n", err)
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

// ListDistilledSessions wraps the memory package function for use here.
// This avoids import cycles by re-exporting through vectordb.
func init() {
	// Register vectordb wrappers
}
