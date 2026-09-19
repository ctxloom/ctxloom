package composite

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBookend_HighestFirstSecondHighestLast(t *testing.T) {
	tests := []struct {
		name     string
		input    []FragmentAsk
		expected []string
	}{
		{
			name:     "empty",
			input:    []FragmentAsk{},
			expected: nil,
		},
		{
			name:     "single fragment",
			input:    []FragmentAsk{{Name: "a", Priority: 5}},
			expected: []string{"a"},
		},
		{
			name:     "two fragments",
			input:    []FragmentAsk{{Name: "low", Priority: 1}, {Name: "high", Priority: 10}},
			expected: []string{"high", "low"}, // Sorted by priority desc
		},
		{
			name: "three fragments - bookend",
			input: []FragmentAsk{
				{Name: "low", Priority: 1},
				{Name: "med", Priority: 5},
				{Name: "high", Priority: 10},
			},
			// Bookend: [highest, middle..., second-highest]
			// high(10) at start, med(5) at end, low(1) in middle
			expected: []string{"high", "low", "med"},
		},
		{
			name: "five fragments - full bookend",
			input: []FragmentAsk{
				{Name: "e", Priority: 1},
				{Name: "d", Priority: 2},
				{Name: "c", Priority: 3},
				{Name: "b", Priority: 4},
				{Name: "a", Priority: 5},
			},
			// Sorted desc: a(5), b(4), c(3), d(2), e(1)
			// Bookend: a at start, b at end, c,d,e fill middle
			expected: []string{"a", "c", "d", "e", "b"},
		},
		{
			name: "same priority - stable order",
			input: []FragmentAsk{
				{Name: "first", Priority: 0},
				{Name: "second", Priority: 0},
				{Name: "third", Priority: 0},
			},
			// All same priority, bookend still applies
			expected: []string{"first", "third", "second"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, r := range bookend(tt.input) {
				got = append(got, r.Name)
			}
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestDedupe_KeepsTheHighestPriorityPerName(t *testing.T) {
	input := []FragmentAsk{
		{Name: "a", Priority: 5},
		{Name: "b", Priority: 3},
		{Name: "a", Priority: 10}, // Higher priority for 'a'
		{Name: "c", Priority: 1},
		{Name: "b", Priority: 2}, // Lower priority for 'b', should be ignored
	}

	result := dedupe(input)

	// Should have 3 unique fragments
	assert.Len(t, result, 3)

	// Find each and check priorities
	priorities := make(map[string]int)
	for _, f := range result {
		priorities[f.Name] = f.Priority
	}

	assert.Equal(t, 10, priorities["a"], "should keep higher priority for 'a'")
	assert.Equal(t, 3, priorities["b"], "should keep first (higher) priority for 'b'")
	assert.Equal(t, 1, priorities["c"])
}
