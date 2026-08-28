package ledger

import (
	"fmt"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newWarnRecordingLedger records the FORMATTED warning, not the format string.
// newLedger keeps only the format, which would let every assertion below pass
// no matter which entries were actually retracted — the message is the whole
// payload of this feature, so the test has to read it.
func newWarnRecordingLedger(t *testing.T) (Ledger, *[]string) {
	t.Helper()
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll("/dir", 0o755))
	var warnings []string
	l := Ledger{
		FS:  fs,
		Dir: "/dir",
		Warn: func(format string, args ...any) {
			warnings = append(warnings, fmt.Sprintf(format, args...))
		},
	}
	return l, &warnings
}

// The ruling this pins: an empty declared set RETRACTS what ctxloom installed
// last round, and says so. Silent retraction is the failure mode — the user
// loses hooks off their disk with nothing to tell them why.
func TestWrite_EmptyingASurface_WarnsNamingEveryRetractedEntry(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceHooks, []string{"digest-a", "digest-b"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceHooks, nil))

	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], "digest-a")
	assert.Contains(t, (*warnings)[0], "digest-b")
	assert.Contains(t, (*warnings)[0], "retracted 2")
}

// A partial loss names only what was actually lost. Naming the survivor too
// would be a false report of destruction.
func TestWrite_DroppingSomeEntries_WarnsAboutOnlyThoseDropped(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceCommands, []string{"keep.md", "gone-a.md", "gone-b.md"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceCommands, []string{"keep.md"}))

	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], "gone-a.md")
	assert.Contains(t, (*warnings)[0], "gone-b.md")
	assert.NotContains(t, (*warnings)[0], "keep.md")
}

// THE LOAD-BEARING NEGATIVE. Every writer removes-then-readds on every single
// run, so a warning keyed to removal rather than NET loss would fire on every
// launch and mean nothing. Without this, the feature is noise.
func TestWrite_RewritingTheSameSet_DoesNotWarn(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceHooks, []string{"digest-a", "digest-b"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceHooks, []string{"digest-b", "digest-a"}))

	assert.Empty(t, *warnings, "remove-then-readd of an unchanged set is not a retraction")
}

func TestWrite_GrowingASurface_DoesNotWarn(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceMCP, []string{"ctxloom"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceMCP, []string{"ctxloom", "taskloom"}))

	assert.Empty(t, *warnings)
}

// A first write has nothing to retract. Warning here would fire on every
// freshly-configured project.
func TestWrite_FirstEverWrite_DoesNotWarn(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)

	require.NoError(t, l.Write(SurfaceHooks, []string{"digest-a"}))

	assert.Empty(t, *warnings)
}

// Retracting one entry must not read as "1 entries". The count is the headline
// of the message, so its grammar is part of the deliverable.
func TestWrite_RetractingExactlyOne_ReadsAsSingular(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceHooks, []string{"only"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceHooks, nil))

	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], "retracted 1 hooks entry")
	assert.NotContains(t, (*warnings)[0], "entries")
}

// Retraction is per-surface: emptying one must not report a co-located
// surface's entries as lost, since they are still on disk.
func TestWrite_EmptyingOneSurface_DoesNotReportACoLocatedSurfacesEntries(t *testing.T) {
	l, warnings := newWarnRecordingLedger(t)
	require.NoError(t, l.Write(SurfaceHooks, []string{"hook-digest"}))
	require.NoError(t, l.Write(SurfaceMCP, []string{"mcp-name"}))
	*warnings = nil

	require.NoError(t, l.Write(SurfaceHooks, nil))

	require.Len(t, *warnings, 1)
	assert.Contains(t, (*warnings)[0], "hook-digest")
	assert.NotContains(t, (*warnings)[0], "mcp-name")
}
