//go:build acceptance

package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/tests/integration/testenv"
)

// TestFileContains_RecordsAnExcerptAsLivingDocsEvidence: the generic
// `the file "…" contains "…"` step must hand the doc-capture sidecar the lines
// it matched — not nothing, which left the living-docs page with no evidence,
// and not the whole file, which buries the match.
func TestFileContains_RecordsAnExcerptAsLivingDocsEvidence(t *testing.T) {
	const rel, match = "notes.md", "the matched line"
	lines := []string{"far above", "above-2", "above-1", match, "below-1", "below-2", "far below"}
	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, rel), []byte(strings.Join(lines, "\n")), 0o600))

	for name, want := range map[string]string{
		"single-line want": match,
		"multi-line want":  match + "\nbelow-1",
	} {
		t.Run(name, func(t *testing.T) {
			w := &World{env: &testenv.TestEnvironment{ProjectDir: project}}
			c := context.WithValue(context.Background(), worldKey{}, w)

			require.NoError(t, fileContains(c, false, rel, want))
			assert.Contains(t, w.docStepMaterialized, match)
			assert.NotContains(t, w.docStepMaterialized, "far above", "evidence is an excerpt, not the whole file")
			assert.NotContains(t, w.docStepMaterialized, "far below", "evidence is an excerpt, not the whole file")
		})
	}
}
