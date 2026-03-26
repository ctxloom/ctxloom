//go:build memory && !vectors

package cmd

import (
	"context"
	"encoding/json"
)

// getVectorTools returns empty when vectors feature is disabled.
func getVectorTools() []mcpToolInfo {
	return nil
}

// toolQueryMemory is a stub when vectors feature is disabled.
func (s *mcpServer) toolQueryMemory(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"error":   "Vector search not available",
		"message": "Build with -tags 'memory,vectors' to enable semantic search",
	}, nil
}

// toolIndexSession is a stub when vectors feature is disabled.
func (s *mcpServer) toolIndexSession(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"error":   "Vector indexing not available",
		"message": "Build with -tags 'memory,vectors' to enable semantic search",
	}, nil
}
