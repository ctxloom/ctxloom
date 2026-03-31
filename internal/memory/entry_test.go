package memory

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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
