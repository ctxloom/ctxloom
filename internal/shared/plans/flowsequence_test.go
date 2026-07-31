package plans

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseFrontmatter_FlowSessions covers the flow-sequence branch at the
// public seam, including the shapes the cross-package round-trip test in
// internal/memory cannot produce: a value containing a comma, and a
// `sessions:` value that is not a sequence at all.
func TestParseFrontmatter_FlowSessions(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    []string
	}{
		{
			name:    "flow list",
			content: "---\nsessions: [alpha-harp, beta-harp]\n---\nbody",
			want:    []string{"alpha-harp", "beta-harp"},
		},
		{
			name:    "flow list with quoted items",
			content: "---\nsessions: [\"alpha-harp\", 'beta-harp']\n---\nbody",
			want:    []string{"alpha-harp", "beta-harp"},
		},
		{
			name:    "a comma inside quotes does not split an item",
			content: "---\nsessions: [\"alpha, actually\", beta-harp]\n---\nbody",
			want:    []string{"alpha, actually", "beta-harp"},
		},
		{
			name:    "empty flow list yields no sessions",
			content: "---\nsessions: []\n---\nbody",
			want:    nil,
		},
		{
			name:    "a non-sequence value is not invented into one entry",
			content: "---\nsessions: alpha-harp\n---\nbody",
			want:    nil,
		},
		{
			name:    "the block form still works",
			content: "---\nsessions:\n  - alpha-harp\n  - beta-harp\n---\nbody",
			want:    []string{"alpha-harp", "beta-harp"},
		},
		{
			name:    "flow list alongside a title",
			content: "---\ntitle: Design\nsessions: [alpha-harp]\n---\nbody",
			want:    []string{"alpha-harp"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, got := ParseFrontmatter(tc.content)
			assert.Equal(t, tc.want, got)
		})
	}
}
