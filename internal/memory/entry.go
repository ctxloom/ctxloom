//go:build memory

package memory

import (
	"encoding/json"
	"time"
)

// EntryType identifies the type of log entry.
type EntryType string

const (
	EntryTypeUser       EntryType = "user"
	EntryTypeAssistant  EntryType = "assistant"
	EntryTypeToolCall   EntryType = "tool_call"
	EntryTypeToolResult EntryType = "tool_result"
	EntryTypeSystem     EntryType = "system"
)

// Entry represents a single log entry in the session log.
type Entry struct {
	Timestamp time.Time       `json:"ts"`
	SessionID string          `json:"session"`
	Type      EntryType       `json:"type"`
	Content   string          `json:"content,omitempty"`
	ToolCall  *ToolCallData   `json:"tool_call,omitempty"`
	ToolResult *ToolResultData `json:"tool_result,omitempty"`
}

// ToolCallData holds information about a tool invocation.
type ToolCallData struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"args,omitempty"`
}

// ToolResultData holds summarized tool result information.
type ToolResultData struct {
	Name          string `json:"name"`
	Summary       string `json:"summary"`
	Truncated     bool   `json:"truncated,omitempty"`
	OriginalSize  int    `json:"original_size,omitempty"`
	IsError       bool   `json:"is_error,omitempty"`
}

// SessionMeta holds metadata about a session.
type SessionMeta struct {
	SessionID        string    `yaml:"session_id"`
	StartedAt        time.Time `yaml:"started_at"`
	EndedAt          time.Time `yaml:"ended_at,omitempty"`
	ProjectPath      string    `yaml:"project_path,omitempty"`
	Profile          string    `yaml:"profile,omitempty"`
	EntryCount       int       `yaml:"entry_count"`
	TokensEstimate   int       `yaml:"tokens_estimate,omitempty"`
	CompactionStatus string    `yaml:"compaction_status,omitempty"` // pending, completed, failed
	CompactionError  string    `yaml:"compaction_error,omitempty"`
	LastCompactedAt  time.Time `yaml:"last_compacted_at,omitempty"`
}

// MarshalJSONL returns the entry as a JSON line (no trailing newline).
func (e *Entry) MarshalJSONL() ([]byte, error) {
	return json.Marshal(e)
}

// UnmarshalJSONL parses a JSON line into an entry.
func UnmarshalJSONL(data []byte) (*Entry, error) {
	var e Entry
	if err := json.Unmarshal(data, &e); err != nil {
		return nil, err
	}
	return &e, nil
}
