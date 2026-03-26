//go:build memory && vectors

package vectordb

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	chromem "github.com/philippgille/chromem-go"
)

const (
	collectionName = "scm_memory"
	dbFileName     = "chromem.db"
)

// ChromemStore implements Store using chromem-go.
type ChromemStore struct {
	db         *chromem.DB
	collection *chromem.Collection
	embedder   Embedder
	dbPath     string
}

// NewChromemStore creates a new ChromemStore.
func NewChromemStore(memoryDir string, embedder Embedder) (*ChromemStore, error) {
	dbPath := filepath.Join(memoryDir, "vectors", dbFileName)

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, fmt.Errorf("create vectors dir: %w", err)
	}

	// Create embedding function wrapper for chromem-go
	embeddingFunc := func(ctx context.Context, text string) ([]float32, error) {
		return embedder.Embed(ctx, text)
	}

	// Open or create the database
	db, err := chromem.NewPersistentDB(dbPath, false)
	if err != nil {
		return nil, fmt.Errorf("open chromem db: %w", err)
	}

	// Get or create collection
	collection, err := db.GetOrCreateCollection(collectionName, nil, embeddingFunc)
	if err != nil {
		return nil, fmt.Errorf("get collection: %w", err)
	}

	return &ChromemStore{
		db:         db,
		collection: collection,
		embedder:   embedder,
		dbPath:     dbPath,
	}, nil
}

// Add stores a memory chunk.
func (s *ChromemStore) Add(ctx context.Context, chunk MemoryChunk) error {
	// Serialize metadata
	metadata := map[string]string{
		"session_id":  chunk.SessionID,
		"chunk_index": fmt.Sprintf("%d", chunk.ChunkIndex),
		"timestamp":   chunk.Timestamp.Format(time.RFC3339),
	}

	if len(chunk.Topics) > 0 {
		topics, _ := json.Marshal(chunk.Topics)
		metadata["topics"] = string(topics)
	}
	if len(chunk.ToolsUsed) > 0 {
		tools, _ := json.Marshal(chunk.ToolsUsed)
		metadata["tools_used"] = string(tools)
	}
	if len(chunk.FilesModified) > 0 {
		files, _ := json.Marshal(chunk.FilesModified)
		metadata["files_modified"] = string(files)
	}

	// Create document
	doc := chromem.Document{
		ID:       chunk.ID,
		Content:  chunk.Content,
		Metadata: metadata,
	}

	// Add to collection
	return s.collection.AddDocument(ctx, doc)
}

// Query searches for similar chunks.
func (s *ChromemStore) Query(ctx context.Context, query string, opts QueryOpts) ([]QueryResult, error) {
	if opts.Limit <= 0 {
		opts.Limit = 5
	}

	// Build where filter for time range and session
	var whereDoc map[string]string
	if opts.SessionID != "" {
		whereDoc = map[string]string{"session_id": opts.SessionID}
	}

	// Query the collection
	results, err := s.collection.Query(ctx, query, opts.Limit, whereDoc, nil)
	if err != nil {
		return nil, fmt.Errorf("query collection: %w", err)
	}

	// Convert results
	var queryResults []QueryResult
	for _, r := range results {
		// Skip results below threshold
		if opts.Threshold > 0 && r.Similarity < opts.Threshold {
			continue
		}

		chunk := MemoryChunk{
			ID:        r.ID,
			Content:   r.Content,
			SessionID: r.Metadata["session_id"],
		}

		// Parse chunk index
		if idx := r.Metadata["chunk_index"]; idx != "" {
			fmt.Sscanf(idx, "%d", &chunk.ChunkIndex)
		}

		// Parse timestamp
		if ts := r.Metadata["timestamp"]; ts != "" {
			chunk.Timestamp, _ = time.Parse(time.RFC3339, ts)
		}

		// Parse arrays
		if topics := r.Metadata["topics"]; topics != "" {
			_ = json.Unmarshal([]byte(topics), &chunk.Topics)
		}
		if tools := r.Metadata["tools_used"]; tools != "" {
			_ = json.Unmarshal([]byte(tools), &chunk.ToolsUsed)
		}
		if files := r.Metadata["files_modified"]; files != "" {
			_ = json.Unmarshal([]byte(files), &chunk.FilesModified)
		}

		// Apply time range filter
		if opts.TimeRange != "" && opts.TimeRange != "all" {
			if !isWithinTimeRange(chunk.Timestamp, opts.TimeRange) {
				continue
			}
		}

		queryResults = append(queryResults, QueryResult{
			Chunk:      chunk,
			Similarity: r.Similarity,
		})
	}

	return queryResults, nil
}

// DeleteSession removes all chunks for a session.
func (s *ChromemStore) DeleteSession(ctx context.Context, sessionID string) error {
	// chromem-go doesn't have a direct delete by filter, so we need to
	// query all documents with this session and delete them individually
	results, err := s.collection.Query(ctx, "", 1000, map[string]string{"session_id": sessionID}, nil)
	if err != nil {
		return fmt.Errorf("query for deletion: %w", err)
	}

	for _, r := range results {
		if err := s.collection.Delete(ctx, nil, nil, r.ID); err != nil {
			return fmt.Errorf("delete document %s: %w", r.ID, err)
		}
	}

	return nil
}

// Close closes the store.
func (s *ChromemStore) Close() error {
	if s.embedder != nil {
		return s.embedder.Close()
	}
	return nil
}

// isWithinTimeRange checks if a timestamp is within the specified range.
func isWithinTimeRange(ts time.Time, timeRange string) bool {
	if ts.IsZero() {
		return true // No timestamp, include by default
	}

	now := time.Now()
	var cutoff time.Time

	switch timeRange {
	case "today":
		cutoff = now.Truncate(24 * time.Hour)
	case "week":
		cutoff = now.AddDate(0, 0, -7)
	case "month":
		cutoff = now.AddDate(0, -1, 0)
	default:
		return true // Unknown range, include all
	}

	return ts.After(cutoff)
}
