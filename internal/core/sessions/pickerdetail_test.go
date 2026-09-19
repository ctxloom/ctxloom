package sessions

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// PickerDetail — extra picker lines from the body's Open Items section.
// =============================================================================

func TestPickerDetail(t *testing.T) {
	longBullet := "- " + strings.Repeat("x", 120)
	tests := []struct {
		name     string
		body     string
		expected []string
	}{
		{
			name:     "extracts open items bullets",
			body:     "### Open Items\n- finish the picker\n- write the tests\n\n### State\nin progress",
			expected: []string{"- finish the picker", "- write the tests"},
		},
		{
			name:     "stops at the next section heading",
			body:     "### Open Items\n- only this one\n\n### Decisions\n- not this one",
			expected: []string{"- only this one"},
		},
		{
			name:     "caps at four bullets",
			body:     "### Open Items\n- one\n- two\n- three\n- four\n- five\n- six",
			expected: []string{"- one", "- two", "- three", "- four"},
		},
		{
			name:     "caps each bullet at 80 bytes",
			body:     "### Open Items\n" + longBullet,
			expected: []string{longBullet[:80]},
		},
		{
			name:     "nil when no open items section",
			body:     "### State\n- this is state, not open items",
			expected: nil,
		},
		{
			name:     "ignores prose before the section",
			body:     "Some intro prose.\n\n### Open Items\n- the real item",
			expected: []string{"- the real item"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, PickerDetail(tt.body))
		})
	}
}
