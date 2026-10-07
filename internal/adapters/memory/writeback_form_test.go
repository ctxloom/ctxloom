package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/yamlform"
)

// TestEssenceSaveIsWriteBackForm: an essence's front-matter is saved in the
// encoding an upgrade write-back would give it. The front-matter is all
// scalars today, so this holds under any indentation; it is here so a nested
// field added later is held to the one encoding.
func TestEssenceSaveIsWriteBackForm(t *testing.T) {
	testsupport.Isolate(t)
	dir := t.TempDir()
	recordOutputDir(t, "fresh-harp")
	c := &Compactor{fs: afero.NewOsFs(), config: CompactionConfig{OutputDir: dir}}
	_, err := c.saveCompacted("fresh", "body", compactedMeta{HarpName: "fresh-harp", Summary: "one line"})
	require.NoError(t, err)

	data, err := os.ReadFile(filepath.Join(dir, "fresh.md"))
	require.NoError(t, err)
	rest, ok := strings.CutPrefix(string(data), "---\n")
	require.True(t, ok, "essence starts with front-matter:\n%s", data)
	fm, _, ok := strings.Cut(rest, "---\n")
	require.True(t, ok, "front-matter is closed:\n%s", data)
	yamlform.RequireWriteBackForm(t, []byte(fm))
}
