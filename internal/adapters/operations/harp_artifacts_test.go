package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/paths"
)

func writeHarpFile(t *testing.T, root, harp, name, body string) string {
	t.Helper()
	dir := filepath.Join(root, harp)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o644))
	return p
}

// TestHarpTopLevelArtifacts_NamesUnclassifiedFilesOnly pins the predicate
// cli.doctorCheckHarpDurability reports through: every top-level row of
// paths.HarpMembers is excluded — flagging one would put a warning on every
// healthy session — and so is every non-regular entry; everything else at the
// top level is a file nobody classified, including a transcript left where
// its row does not live.
func TestHarpTopLevelArtifacts_NamesUnclassifiedFilesOnly(t *testing.T) {
	root := t.TempDir()
	const harp = "brisk-teal-otter"
	writeHarpFile(t, root, harp, "design"+paths.PlanFileExt, "authored")
	writeHarpFile(t, root, harp, "audit.md", "authored")
	writeHarpFile(t, root, harp, paths.CanonicalTranscriptFileName, "not where its row lives")
	for _, m := range paths.HarpMembers {
		if m.Location != paths.AtTop {
			continue
		}
		switch m.Name {
		case paths.SessionEngineHomesDirName, paths.PersistDirName, paths.EphemeralDirName, paths.SegmentsDirName:
			require.NoError(t, os.MkdirAll(filepath.Join(root, harp, m.Name), 0o755))
		default:
			writeHarpFile(t, root, harp, m.Name, "ctxloom's own")
		}
	}
	require.NoError(t, os.Symlink(filepath.Join(root, "elsewhere.jsonl"), filepath.Join(root, harp, paths.EngineTranscriptLinkPrefix+"claude-abc.jsonl")))

	got, err := HarpTopLevelArtifacts(filepath.Join(root, harp))
	require.NoError(t, err)
	assert.Equal(t, []string{"audit.md", "design" + paths.PlanFileExt, paths.CanonicalTranscriptFileName}, got)
}

func TestHarpTopLevelArtifacts_MissingDirIsNotAFault(t *testing.T) {
	got, err := HarpTopLevelArtifacts(filepath.Join(t.TempDir(), "never-existed"))
	require.NoError(t, err)
	assert.Empty(t, got)
}
