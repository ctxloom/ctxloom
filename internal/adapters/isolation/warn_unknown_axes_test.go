package isolation

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/stretchr/testify/require"
)

// isolation.warnUnknownAxes decides, per axis, whether a value the user typed
// is one ctxloom knows. Both of its guards were entirely unverified: every
// operand could be replaced with `true` and the suite stayed green.
//
// The SILENT cases are what make this test bite. Asserting only that a bogus
// value complains would pass with either guard stuck at `true` — it is the
// known values staying quiet that pins the condition.
func TestWarnUnknownAxes_WorkspaceKnownValuesStaySilent(t *testing.T) {
	for _, ws := range []WorkspaceAxis{"", WorkspaceShared, WorkspaceWorktree} {
		t.Run(string("ws="+ws), func(t *testing.T) {
			var sink bytes.Buffer
			restore := clidiag.SetSink(&sink)
			defer restore()

			warnUnknownAxes(Axes{Workspace: ws})

			require.NotContains(t, sink.String(), "unknown workspace axis",
				"%q is a known workspace value and must not be reported as unknown", ws)
		})
	}
}

func TestWarnUnknownAxes_UnknownWorkspaceIsNamed(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	warnUnknownAxes(Axes{Workspace: "definitely-not-a-workspace"})

	out := sink.String()
	require.Contains(t, out, "unknown workspace axis", "an unrecognised workspace must be reported")
	require.Contains(t, out, "definitely-not-a-workspace",
		"the message must name the value the user typed, or they cannot find their typo")
}

// The RUNTIME arm is not diagnostics: an unrecognised runtime means the run
// would land on the HOST with no container boundary, so warnUnknownAxes raises
// a ClassIsolation finding rather than a warning. Every known runtime must
// therefore raise NOTHING — a guard stuck at `true` would abort ordinary runs.
func TestWarnUnknownAxes_KnownRuntimesRaiseNoFinding(t *testing.T) {
	for _, rt := range []RuntimeAxis{"", RuntimeHost, RuntimeContainerRootless, RuntimeContainerRootful} {
		t.Run("runtime="+string(rt), func(t *testing.T) {
			var sink bytes.Buffer
			restore := clidiag.SetSink(&sink)
			defer restore()

			mark := strictness.Checkpoint()
			warnUnknownAxes(Axes{Runtime: rt})

			require.Empty(t, strictness.Since(mark),
				"%q is a known runtime; raising a finding here would abort ordinary runs", rt)
		})
	}
}

func TestWarnUnknownAxes_UnknownRuntimeIsAFatalIsolationFinding(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	defer restore()

	mark := strictness.Checkpoint()
	warnUnknownAxes(Axes{Runtime: "definitely-not-a-runtime"})

	found := strictness.Since(mark)
	require.Len(t, found, 1, "an unrecognised runtime must raise exactly one finding")
	require.Equal(t, strictness.ClassIsolation, found[0].Class,
		"the hazard is landing unsandboxed on the host, which is an isolation class finding")
	require.Contains(t, strings.ToLower(found[0].Message), "definitely-not-a-runtime",
		"the finding must name the value the user typed")
}
