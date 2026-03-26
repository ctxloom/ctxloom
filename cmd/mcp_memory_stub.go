//go:build !memory

package cmd

import (
	"context"
	"encoding/json"
)

// getMemoryTools returns empty list when memory feature is disabled.
func (s *mcpServer) getMemoryTools() []mcpToolInfo {
	return nil
}

// toolCompactSession is a stub when memory feature is disabled.
func (s *mcpServer) toolCompactSession(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"error": "Memory feature not enabled. Build with -tags memory to enable.",
	}, nil
}

// toolGetSessionMemory is a stub when memory feature is disabled.
func (s *mcpServer) toolGetSessionMemory(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"loaded":  false,
		"message": "Memory feature not enabled. Build with -tags memory to enable.",
	}, nil
}

// toolListSessions is a stub when memory feature is disabled.
func (s *mcpServer) toolListSessions(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"sessions": []interface{}{},
		"count":    0,
		"message":  "Memory feature not enabled. Build with -tags memory to enable.",
	}, nil
}

// toolQueryMemory is a stub when memory feature is disabled.
func (s *mcpServer) toolQueryMemory(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"error":   "Memory feature not enabled",
		"message": "Build with -tags memory to enable.",
	}, nil
}

// toolIndexSession is a stub when memory feature is disabled.
func (s *mcpServer) toolIndexSession(_ context.Context, _ json.RawMessage) (interface{}, error) {
	return map[string]interface{}{
		"error":   "Memory feature not enabled",
		"message": "Build with -tags memory to enable.",
	}, nil
}
