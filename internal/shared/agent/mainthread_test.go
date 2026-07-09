package agent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestMainThreadEntries: the shared main-thread filter drops exactly the
// sidechain-marked entries, preserving order — the view distillation and
// session-load replay consume.
func TestMainThreadEntries(t *testing.T) {
	entries := []SessionEntry{
		{Type: EntryTypeUser, Content: "ask"},
		{Type: EntryTypeAssistant, Content: "interior", Sidechain: true},
		{Type: EntryTypeToolUse, ToolName: "Grep", Sidechain: true},
		{Type: EntryTypeAssistant, Content: "answer"},
	}

	main := MainThreadEntries(entries)
	assert.Equal(t, []SessionEntry{entries[0], entries[3]}, main)

	assert.Empty(t, MainThreadEntries([]SessionEntry{{Content: "x", Sidechain: true}}))
	assert.Empty(t, MainThreadEntries(nil))
}
