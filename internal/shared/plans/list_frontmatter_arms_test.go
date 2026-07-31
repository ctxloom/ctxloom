package plans

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
)

// TestList_FrontmatterArms characterizes every arm of the title/sessions
// decision List makes per plan. It replaces the compound guard
// `if t, ss := ParseFrontmatter(...); t != "" || len(ss) > 0` that used to
// wrap the whole assignment: that condition was doing no work, because the
// empty case assigned the same values the fallbacks already held, and it hid
// the two independent decisions (does the title fall back to the file name;
// what are the sessions) behind one boolean.
//
// Behaviour is what is pinned, not shape, so this stays green across any
// further tidying of the parser.
func TestList_FrontmatterArms(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantTitle    string
		wantSessions []string
	}{
		{
			name:         "title and sessions",
			content:      "---\ntitle: Real Title\nsessions:\n  - alpha-harp\n---\nbody\n",
			wantTitle:    "Real Title",
			wantSessions: []string{"alpha-harp"},
		},
		{
			name:         "title only: sessions stay nil",
			content:      "---\ntitle: Real Title\n---\nbody\n",
			wantTitle:    "Real Title",
			wantSessions: nil,
		},
		{
			name:         "sessions only: title falls back to the file name",
			content:      "---\nsessions:\n  - alpha-harp\n---\nbody\n",
			wantTitle:    "design",
			wantSessions: []string{"alpha-harp"},
		},
		{
			name:         "frontmatter with neither: both fall back",
			content:      "---\nstatus: draft\n---\nbody\n",
			wantTitle:    "design",
			wantSessions: nil,
		},
		{
			name:         "empty title value does not blank the fallback",
			content:      "---\ntitle:\nsessions:\n  - alpha-harp\n---\nbody\n",
			wantTitle:    "design",
			wantSessions: []string{"alpha-harp"},
		},
		{
			name:         "no frontmatter at all",
			content:      "# design\n\nbody\n",
			wantTitle:    "design",
			wantSessions: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			harpDir := filepath.Join(root, "vital-deaf-stunt")
			require.NoError(t, os.MkdirAll(harpDir, 0o755))
			require.NoError(t, os.WriteFile(
				filepath.Join(harpDir, "design"+paths.PlanFileExt), []byte(tc.content), 0o644))

			got, err := List(root)
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.wantTitle, got[0].Title)
			assert.Equal(t, tc.wantSessions, got[0].Sessions)
		})
	}
}
