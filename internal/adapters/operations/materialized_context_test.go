package operations

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
)

// claimIn stages writer's claim on path in rec.
func claimIn(t *testing.T, fs afero.Fs, rec delivery.Ownership, path string, w delivery.Writer) {
	t.Helper()
	b := safefs.NewBatch(fs, func(_ string, fn func() error) error { return fn() })
	require.NoError(t, rec.In(b).Stage(path, w, []present.Claim{{Pointer: present.AppendedSection, Value: []byte("ctx")}}))
	_, err := b.Commit()
	require.NoError(t, err)
}

// TestMaterializedContextFindings (materialize tests 34-36): one advisory
// finding, naming the file and the remedy, per file an at-rest context
// writer claims under the project; none for a claim under another
// directory, for another kind, or for a session's own claims.
func TestMaterializedContextFindings(t *testing.T) {
	fs := afero.NewMemMapFs()
	rec, err := fsstatic.NewRecords(fs, "/records")
	require.NoError(t, err)
	fam := delivery.ProjectWriterFor("mock")
	claimIn(t, fs, rec, "/elsewhere/CTX.md", fam.Of(present.Context))
	claimIn(t, fs, rec, "/p/settings.json", fam.Of(present.Settings))
	claimIn(t, fs, rec, "/p/SESSION.md", delivery.SessionWriter("brisk-otter"))
	require.Empty(t, MaterializedContextFindings(rec, "/p"), "no context claim under the project: no warning")

	claimIn(t, fs, rec, "/p/docs/AGENTS.md", fam.Of(present.Context))
	got := MaterializedContextFindings(rec, "/p")
	require.Len(t, got, 1)
	require.False(t, got[0].Fatal(), "a warning, not a refusal")
	require.Contains(t, got[0].Text, "/p/docs/AGENTS.md")
	require.Contains(t, got[0].Remedy, "ctxloom materialize --release --surface context")
}

// TestMaterializedContextFindings_NoRecordOrNoProjectIsNoFinding: with no
// ownership record, or no project root, there is nothing to warn about and
// nothing is read.
func TestMaterializedContextFindings_NoRecordOrNoProjectIsNoFinding(t *testing.T) {
	require.Empty(t, MaterializedContextFindings(nil, "/p"), "no record")

	fs := afero.NewMemMapFs()
	rec, err := fsstatic.NewRecords(fs, "/records")
	require.NoError(t, err)
	claimIn(t, fs, rec, "/p/AGENTS.md", delivery.ProjectWriterFor("mock").Of(present.Context))
	require.Empty(t, MaterializedContextFindings(rec, ""), "no project root")
}
