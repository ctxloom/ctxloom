package plans

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// List walks every plan, so a duplicate-key warning that does not name its
// file is unactionable: the user sees the line and cannot tell which of many
// plans produced it. Two plans, only one with a duplicate — the single
// warning must name that one and not the other.
func TestList_DuplicateKeyWarningNamesThePlanFile(t *testing.T) {
	root := t.TempDir()
	write := func(harp, name, content string) string {
		dir := filepath.Join(root, harp)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		p := filepath.Join(dir, name+paths.PlanFileExt)
		require.NoError(t, os.WriteFile(p, []byte(content), 0o644))
		return p
	}
	dup := write("harp-dup", "dupe", "---\ntitle: A\ntitle: B\n---\n")
	clean := write("harp-clean", "clean", "---\ntitle: C\n---\n")

	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()

	got, err := listRoot(t, root)
	require.NoError(t, err)
	require.Len(t, got, 2)

	lines := warningLines(buf.String())
	require.Len(t, lines, 1, "one duplicated key in one plan is one warning, got:\n%s", buf.String())
	assert.Equal(t, dup+`: duplicate frontmatter key "title"; using the last value`, lines[0],
		"the warning must name the plan file it came from")
	assert.NotContains(t, buf.String(), clean, "the clean plan must not be named")
}
