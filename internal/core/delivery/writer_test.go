package delivery_test

import (
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/delivery"
	"github.com/ctxloom/ctxloom/internal/core/present"
)

// TestWriter_SessionHarp: the harp a session's writer tag names is what a
// sweep probes for liveness; the project's writer, and a session tag naming
// no harp, name none.
func TestWriter_SessionHarp(t *testing.T) {
	harp, ok := delivery.SessionWriter("brave-amber-fox").SessionHarp()
	require.True(t, ok)
	require.Equal(t, "brave-amber-fox", harp)

	_, ok = delivery.ProjectWriter.SessionHarp()
	require.False(t, ok, "the project writer is no session")

	_, ok = delivery.SessionWriter("").SessionHarp()
	require.False(t, ok, "a session tag naming no harp names no session to probe")
}

// TestWriter_ProjectFamilyIsPerEngineAndKind (materialize test 4): the
// at-rest writer is one tag per (engine, kind), so a delivery of one kind
// or one engine never releases another's claims. Project() admits the
// family and the legacy bare tag; no project tag names a session.
func TestWriter_ProjectFamilyIsPerEngineAndKind(t *testing.T) {
	fam := delivery.ProjectWriterFor("claude-code")
	require.Equal(t, delivery.Writer("project:claude-code"), fam)
	require.Equal(t, delivery.Writer("project:claude-code:context"), fam.Of(present.Context))
	require.Equal(t, delivery.Writer("project:claude-code:skills"), fam.Of(present.Skills))

	for _, w := range []delivery.Writer{delivery.ProjectWriter, fam, fam.Of(present.Hooks)} {
		require.True(t, w.Project(), "%s is a project writer", w)
		_, ok := w.SessionHarp()
		require.False(t, ok, "%s names no session", w)
	}
	for _, w := range []delivery.Writer{delivery.SessionWriter("x"), "projects", "projectx:y"} {
		require.False(t, w.Project(), "%s is no project writer", w)
	}
}

// TestAllKinds_IsEveryKindInPlanOrder: the six kinds, in the order the
// at-rest planner walks them.
func TestAllKinds_IsEveryKindInPlanOrder(t *testing.T) {
	require.Equal(t, []present.Kind{present.Context, present.MCP, present.Settings, present.Hooks, present.Commands, present.Skills}, delivery.AllKinds())
}

// TestTarget_WritersSplitPerKindOnlyWhenKindsAreNamed (test 5's other
// half): a target naming no kinds is one writer, exactly as before; one
// naming kinds speaks for each of them under its own tag and for nothing
// else.
func TestTarget_WritersSplitPerKindOnlyWhenKindsAreNamed(t *testing.T) {
	session := delivery.Target{Writer: delivery.SessionWriter("h")}
	require.Equal(t, []delivery.Writer{delivery.SessionWriter("h")}, session.Writers())
	require.Equal(t, delivery.SessionWriter("h"), session.WriterOf(present.Skills))

	fam := delivery.ProjectWriterFor("mock")
	scoped := delivery.Target{Writer: fam, Kinds: []present.Kind{present.Context, present.Hooks}}
	require.Equal(t, []delivery.Writer{fam.Of(present.Context), fam.Of(present.Hooks)}, scoped.Writers())
	require.Equal(t, fam.Of(present.Hooks), scoped.WriterOf(present.Hooks))
}

// TestTarget_ValidateRefusesAnUnknownKind: a kind outside the vocabulary
// would name a writer no later run ever speaks for again.
func TestTarget_ValidateRefusesAnUnknownKind(t *testing.T) {
	base := delivery.Target{Root: present.ProjectOnHost(t.TempDir()), Ownership: newRecord(t, afero.NewMemMapFs()), Writer: delivery.ProjectWriterFor("mock")}
	require.NoError(t, base.Validate())
	ok := base
	ok.Kinds = delivery.AllKinds()
	require.NoError(t, ok.Validate())
	bad := base
	bad.Kinds = []present.Kind{present.Context, present.Kind(99)}
	require.ErrorIs(t, bad.Validate(), delivery.ErrNoRoot)
}
