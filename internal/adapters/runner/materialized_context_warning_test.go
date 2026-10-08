package runner

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
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

// TestWarnMaterializedContext (materialize tests 34-36): a run warns once,
// naming the file and the remedy, when an at-rest context writer claims a
// file under its project; not for a claim under another directory, not for
// another kind, and never for a session's own claims.
func TestWarnMaterializedContext(t *testing.T) {
	fs := afero.NewMemMapFs()
	rec, err := fsstatic.NewRecords(fs, "/records")
	require.NoError(t, err)
	fam := delivery.ProjectWriterFor("mock")
	claimIn(t, fs, rec, "/elsewhere/CTX.md", fam.Of(present.Context))
	claimIn(t, fs, rec, "/p/settings.json", fam.Of(present.Settings))
	claimIn(t, fs, rec, "/p/SESSION.md", delivery.SessionWriter("brisk-otter"))

	var got report.Findings
	warnMaterializedContext(rec, "/p", &got)
	require.Empty(t, got, "no context claim under the project: no warning")

	claimIn(t, fs, rec, "/p/docs/AGENTS.md", fam.Of(present.Context))
	warnMaterializedContext(rec, "/p", &got)
	require.Len(t, got, 1)
	require.False(t, got[0].Fatal(), "a warning, not a refusal")
	require.Contains(t, got[0].Text, "/p/docs/AGENTS.md")
	require.Contains(t, got[0].Remedy, "ctxloom materialize --release --surface context")
}
