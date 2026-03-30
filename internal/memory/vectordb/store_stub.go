//go:build !vectors

package vectordb

import (
	"context"
	"fmt"
)

// ChromemStore is a stub when vectors feature is disabled.
type ChromemStore struct{}

// NewChromemStore returns an error when vectors feature is disabled.
func NewChromemStore(memoryDir string, embedder Embedder) (*ChromemStore, error) {
	return nil, fmt.Errorf("vector database not available: build with -tags 'memory,vectors' to enable")
}

// Add is a stub.
func (s *ChromemStore) Add(_ context.Context, _ MemoryChunk) error {
	return fmt.Errorf("vector database not available")
}

// Query is a stub.
func (s *ChromemStore) Query(_ context.Context, _ string, _ QueryOpts) ([]QueryResult, error) {
	return nil, fmt.Errorf("vector database not available")
}

// DeleteSession is a stub.
func (s *ChromemStore) DeleteSession(_ context.Context, _ string) error {
	return fmt.Errorf("vector database not available")
}

// Close is a stub.
func (s *ChromemStore) Close() error {
	return nil
}
