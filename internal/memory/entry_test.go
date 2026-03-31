package memory

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntry_MarshalJSONL(t *testing.T) {
	entry := Entry{
		Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		SessionID: "test-session",
		Type:      EntryTypeUser,
		Content:   "Hello, world!",
	}

	data, err := entry.MarshalJSONL()
	require.NoError(t, err)

	// Verify it's valid JSON
	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "test-session", parsed["session"])
	assert.Equal(t, "user", parsed["type"])
	assert.Equal(t, "Hello, world!", parsed["content"])
}

func TestEntry_MarshalJSONL_WithToolCall(t *testing.T) {
	entry := Entry{
		Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		SessionID: "test-session",
		Type:      EntryTypeToolCall,
		ToolCall: &ToolCallData{
			Name:      "Read",
			Arguments: json.RawMessage(`{"path": "/test/file.txt"}`),
		},
	}

	data, err := entry.MarshalJSONL()
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "tool_call", parsed["type"])
	toolCall := parsed["tool_call"].(map[string]interface{})
	assert.Equal(t, "Read", toolCall["name"])
}

func TestEntry_MarshalJSONL_WithToolResult(t *testing.T) {
	entry := Entry{
		Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
		SessionID: "test-session",
		Type:      EntryTypeToolResult,
		ToolResult: &ToolResultData{
			Name:         "Read",
			Summary:      "[Read 50 lines from /test/file.txt]",
			Truncated:    true,
			OriginalSize: 5000,
			IsError:      false,
		},
	}

	data, err := entry.MarshalJSONL()
	require.NoError(t, err)

	var parsed map[string]interface{}
	err = json.Unmarshal(data, &parsed)
	require.NoError(t, err)

	assert.Equal(t, "tool_result", parsed["type"])
	toolResult := parsed["tool_result"].(map[string]interface{})
	assert.Equal(t, "Read", toolResult["name"])
	assert.Equal(t, true, toolResult["truncated"])
}

func TestUnmarshalJSONL(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected Entry
	}{
		{
			name:  "user entry",
			input: `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"user","content":"Hello"}`,
			expected: Entry{
				Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				SessionID: "test",
				Type:      EntryTypeUser,
				Content:   "Hello",
			},
		},
		{
			name:  "assistant entry",
			input: `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"assistant","content":"Hi there!"}`,
			expected: Entry{
				Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				SessionID: "test",
				Type:      EntryTypeAssistant,
				Content:   "Hi there!",
			},
		},
		{
			name:  "system entry",
			input: `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"system","content":"Session started"}`,
			expected: Entry{
				Timestamp: time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
				SessionID: "test",
				Type:      EntryTypeSystem,
				Content:   "Session started",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry, err := UnmarshalJSONL([]byte(tt.input))
			require.NoError(t, err)

			assert.Equal(t, tt.expected.SessionID, entry.SessionID)
			assert.Equal(t, tt.expected.Type, entry.Type)
			assert.Equal(t, tt.expected.Content, entry.Content)
		})
	}
}

func TestUnmarshalJSONL_Invalid(t *testing.T) {
	_, err := UnmarshalJSONL([]byte("not json"))
	assert.Error(t, err)
}

func TestUnmarshalJSONL_ToolCall(t *testing.T) {
	input := `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"tool_call","tool_call":{"name":"Bash","args":{"command":"ls"}}}`

	entry, err := UnmarshalJSONL([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, EntryTypeToolCall, entry.Type)
	require.NotNil(t, entry.ToolCall)
	assert.Equal(t, "Bash", entry.ToolCall.Name)
}

func TestUnmarshalJSONL_ToolResult(t *testing.T) {
	input := `{"ts":"2024-01-15T10:00:00Z","session":"test","type":"tool_result","tool_result":{"name":"Bash","summary":"[output]","truncated":true,"original_size":1000}}`

	entry, err := UnmarshalJSONL([]byte(input))
	require.NoError(t, err)

	assert.Equal(t, EntryTypeToolResult, entry.Type)
	require.NotNil(t, entry.ToolResult)
	assert.Equal(t, "Bash", entry.ToolResult.Name)
	assert.True(t, entry.ToolResult.Truncated)
	assert.Equal(t, 1000, entry.ToolResult.OriginalSize)
}

func TestEntryType_Constants(t *testing.T) {
	// Verify entry types match expected string values
	assert.Equal(t, EntryType("user"), EntryTypeUser)
	assert.Equal(t, EntryType("assistant"), EntryTypeAssistant)
	assert.Equal(t, EntryType("tool_call"), EntryTypeToolCall)
	assert.Equal(t, EntryType("tool_result"), EntryTypeToolResult)
	assert.Equal(t, EntryType("system"), EntryTypeSystem)
}

func TestSessionMeta_Fields(t *testing.T) {
	meta := SessionMeta{
		SessionID:        "test-123",
		StartedAt:        time.Now(),
		ProjectPath:      "/test/project",
		Profile:          "default",
		EntryCount:       100,
		TokensEstimate:   5000,
		CompactionStatus: "completed",
	}

	assert.Equal(t, "test-123", meta.SessionID)
	assert.Equal(t, "/test/project", meta.ProjectPath)
	assert.Equal(t, "default", meta.Profile)
	assert.Equal(t, 100, meta.EntryCount)
	assert.Equal(t, "completed", meta.CompactionStatus)
}
