package operations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/fsstatic"
	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/shared/safefs"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// notOursKinds are the kinds whose files an earlier ctxloom wrote whole, under
// a writer tag the record no longer honours. The settings kind is not among
// them: its claude writer claims no statusline or env value it did not write
// (settingsClaims), so an earlier ctxloom's value there is left standing, not
// refused.
var notOursKinds = []present.Kind{present.Commands, present.Skills}

// unrecordedOutput is a project holding, with no ownership record, what a
// delivery of every kind writes, each whole file changed: an earlier
// ctxloom's output (or the user's own) at every file the commands and skills
// kinds claim.
func unrecordedOutput(t *testing.T) (afero.Fs, string) {
	t.Helper()
	kind := lookupEngine(t, "claude-code")
	dir := t.TempDir()
	written := afero.NewMemMapFs()
	deliverAt(t, written, dir, kind, everyKindPackage(t, "body"), delivery.AllKinds()...)
	fs := afero.NewMemMapFs()
	var whole int
	require.NoError(t, afero.Walk(written, dir, func(path string, info os.FileInfo, err error) error {
		require.NoError(t, err)
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		require.NoError(t, err)
		body := placedString(t, written, dir, rel)
		if rel != claude.ContextFileName && rel != claude.MCPFileName && rel != claudeSettings {
			whole++
			body += "\nan older build's line\n"
		}
		testsupport.WriteFileString(t, fs, path, body, 0o644)
		return nil
	}))
	require.GreaterOrEqual(t, whole, len(notOursKinds), "a command and a skill file were written")
	return fs, dir
}

// TestPreview_UnrecordedOutputIsRefusedWithTheNotOursFix: a dry run over a
// project holding commands, skills and settings ctxloom has no record of
// refuses each of those kinds as ErrNotOurs, naming the file, with the fix
// that fits both an earlier ctxloom's output and the user's own value.
// MUTATION: refusalFix's ErrNotOurs case returns another remedy → red; a
// kind refused through another error → red.
func TestPreview_UnrecordedOutputIsRefusedWithTheNotOursFix(t *testing.T) {
	fs, dir := unrecordedOutput(t)
	kind := lookupEngine(t, "claude-code")
	var out EngineOutcome

	_, err := previewMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), atRestPlacement(dir, kind.Root().Name, delivery.AllKinds()), &out)

	require.NoError(t, err)
	refused := map[string]KindRefusal{}
	for _, r := range out.Refused {
		refused[r.Kind] = r
	}
	for _, k := range notOursKinds {
		r, ok := refused[k.String()]
		require.True(t, ok, "%v is refused: %+v", k, out.Refused)
		assert.Contains(t, r.Reason, fsstatic.ErrNotOurs.Error(), "%v", k)
		assert.Contains(t, r.Reason, dir, "%v names the file", k)
		assert.Equal(t, notOursFix, r.Fix, "%v", k)
	}
}

// TestDeliver_UnrecordedOutputFailsWithTheNotOursFix: the apply over the
// same project fails with the same fix as its remedy, not the generic one.
// MUTATION: deliverMaterializedAt's remedy ignores ErrNotOurs → red.
func TestDeliver_UnrecordedOutputFailsWithTheNotOursFix(t *testing.T) {
	fs, dir := unrecordedOutput(t)
	kind := lookupEngine(t, "claude-code")
	for _, k := range notOursKinds {
		t.Run(k.String(), func(t *testing.T) {
			out := EngineOutcome{Engine: "claude-code"}
			mark := strictness.Checkpoint()
			t.Cleanup(func() { strictness.Close(mark) })

			deliverMaterializedAt(context.Background(), safefs.NewMem(fs), kind, everyKindPackage(t, "body"), atRestPlacement(dir, kind.Root().Name, []present.Kind{k}), &out)

			found := strictness.Since(mark)
			require.Len(t, found, 1, "%+v", out)
			assert.Contains(t, found[0].Text, fsstatic.ErrNotOurs.Error())
			assert.Equal(t, notOursFix, found[0].Remedy)
		})
	}
}

// TestWriteFailureFix_OnlyNotOursGetsTheNotOursFix: any other write failure
// keeps the generic remedy. MUTATION: the ErrNotOurs test always true → red.
func TestWriteFailureFix_OnlyNotOursGetsTheNotOursFix(t *testing.T) {
	assert.Equal(t, writeFailureRemedy, writeFailureFix(errors.New("disk full")))
	assert.Equal(t, notOursFix, writeFailureFix(fmt.Errorf("deliver: %w", &fsstatic.NotOursError{Target: "/p/f"})))
}
