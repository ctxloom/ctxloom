package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The launch forms replaced a flag that BYPASSED Setup. These tests hold the
// three properties that made the replacement worth doing: each form is selected
// in exactly one place, a form that presents writes nothing, and a form that
// presents a surface that is not there REFUSES instead of quietly doing
// something else.

// TestLaunchFormForCell pins the selection rule for a fan-out member. It is the
// ONLY place the shared-vs-isolated decision is made now; everything downstream
// receives the answer.
func TestLaunchFormForCell(t *testing.T) {
	assert.Equal(t, LaunchFormPresent, LaunchFormForCell(CellKindShared),
		"a member in the shared cwd must present the session's surfaces, never write per-member config over them")
	assert.Equal(t, LaunchFormDeliver, LaunchFormForCell(CellKindDirectoryIsolated),
		"a worktree member has a private cwd, so it delivers its own")
	assert.Equal(t, LaunchFormDeliver, LaunchFormForCell(CellKindProcessIsolated),
		"a container member likewise")
}

// TestSetup_PresentForm_NamesWithoutWriting is the form's whole point: the
// selection is built exactly as a delivering run builds it — same constructors,
// same inputs — and then NOTHING is written. Asserting the constructors still
// ran is what separates "presented" from "skipped": a run that resolved no
// surfaces could not name any either, which is the bypass this replaced.
func TestSetup_PresentForm_NamesWithoutWriting(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "project rules"}},
		CellKind:  CellKindShared,
		Env:       map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Form:      LaunchFormPresent,
		Managed: &ManagedConfig{
			Hooks: &wire.HooksConfig{},
		},
	}))

	assert.NotZero(t, set.built, "every surface is still CONSTRUCTED — a run that resolved nothing could name nothing")
	assert.Len(t, set.presentStarts, 5, "every surface was named")
	assert.Empty(t, set.deliverStarts, "a presenting run must never run the well-known write")
	assert.Empty(t, set.realizeStarts, "nor the out-of-cwd write — the session already did it")
	assert.Empty(t, b.delivered,
		"no handle is recorded: this run wrote nothing, so its Cleanup must not retract a surface the session still needs")
	assert.NotNil(t, b.Resolved(), "the resolved selection is what the engine reads to build argv")
}

// TestSetup_PresentForm_AbsentSurfaceRefuses is THE hazard. "Use the existing
// surface" has exactly one honest failure — there is no existing surface — and
// the tempting answers are both silent degrades: write it (clobbering the one
// copy every run in the session reads) or route the content somewhere else.
// Neither is available; the run fails, naming what is missing.
func TestSetup_PresentForm_AbsentSurfaceRefuses(t *testing.T) {
	var order []string
	set := &recordSet{
		order:      &order,
		presentErr: fmt.Errorf("%w: the settings surface is not at /nope/settings.json", ErrAbsentSharedSurface),
	}
	b := newCellBackend(set)

	err := b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		CellKind: CellKindShared,
		Env:      map[string]string{SessionHarpEnv: "perky-same-chevy"},
		Form:     LaunchFormPresent,
		Managed:  &ManagedConfig{Hooks: &wire.HooksConfig{}},
	})

	require.Error(t, err, "a missing shared surface must REFUSE, never fall back to writing it")
	assert.ErrorIs(t, err, ErrAbsentSharedSurface)
	assert.Contains(t, err.Error(), "/nope/settings.json", "the refusal names what is missing")
	assert.Empty(t, set.deliverStarts, "and it must not have written anything on the way to failing")
	assert.Empty(t, set.realizeStarts)
}

// TestSetup_MinimalForm_ResolvesPostureAndDeliversNothing pins the form that
// replaced the bypass outright. Setup RUNS — that is the change — and what it
// resolves is the engine's declared minimal argv. Nothing is constructed and
// nothing is written, so the difference between "no surfaces" and "surfaces I
// skipped" stays visible.
func TestSetup_MinimalForm_ResolvesPostureAndDeliversNothing(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)
	b.SetMinimalLaunch(MinimalArgsFunc(func(model string) []string {
		return []string{"--bare", "--model", model}
	}))

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:   t.TempDir(),
		Fragments: []*Fragment{{Content: "never delivered"}},
		Form:      LaunchFormMinimal,
		Model:     "haiku",
	}))

	assert.Equal(t, []string{"--bare", "--model", "haiku"}, b.MinimalArgs(),
		"the engine's declared posture is resolved by Setup, so no argv site has to re-derive it from a flag")
	assert.Zero(t, set.built, "a minimal run declares NO surfaces — none are even constructed")
	assert.Empty(t, set.deliverStarts)
	assert.Empty(t, set.realizeStarts)
	assert.Empty(t, set.presentStarts)
	assert.Nil(t, b.Resolved(), "and there is no resolved selection for an argv site to read")
}

// TestSetup_MinimalForm_NeedsNoManagedPayload pins that the minimal form is a
// DECLARATION rather than the failed-config nil. A minimal run carries no
// ManagedConfig and must still resolve its posture — if it went through the
// nil-payload gate instead, a headless run would come back with no argv at all
// and launch the engine bare while reporting success.
func TestSetup_MinimalForm_NeedsNoManagedPayload(t *testing.T) {
	b := newCellBackend(&recordSet{order: new([]string)})
	b.SetMinimalLaunch(MinimalArgsFunc(func(string) []string { return []string{"--bare"} }))

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir: t.TempDir(),
		Form:    LaunchFormMinimal,
		Managed: nil,
	}))
	assert.Equal(t, []string{"--bare"}, b.MinimalArgs())
}

// TestSetup_MinimalForm_UndeclaredPostureLaunchesBare pins the safe direction
// for an engine that declares no minimal posture: no argv, not a panic and not
// a borrowed one from another engine.
func TestSetup_MinimalForm_UndeclaredPostureLaunchesBare(t *testing.T) {
	b := newCellBackend(&recordSet{order: new([]string)})

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir: t.TempDir(),
		Form:    LaunchFormMinimal,
	}))
	assert.Nil(t, b.MinimalArgs())
}

// TestSetup_DeliverForm_IsTheDefault pins that a caller who says nothing about
// the form gets the one that writes its own surfaces. The zero value has to be
// the safe end: a request that silently defaulted to "present" would name
// surfaces nobody delivered, and one that defaulted to "minimal" would launch
// context-free.
func TestSetup_DeliverForm_IsTheDefault(t *testing.T) {
	var order []string
	set := &recordSet{order: &order}
	b := newCellBackend(set)

	require.NoError(t, b.Setup(context.Background(), &SetupRequest{
		WorkDir:  t.TempDir(),
		CellKind: CellKindDirectoryIsolated,
		Managed:  &ManagedConfig{Hooks: &wire.HooksConfig{}},
	}))

	assert.Len(t, set.deliverStarts, 5, "an unset form delivers, exactly as every run did before the form existed")
	assert.Empty(t, set.presentStarts)
}

// TestRequireDelivered is the shared entry check every engine's PresentExisting
// routes through, so they all refuse the same way instead of each inventing its
// own stat.
func TestRequireDelivered(t *testing.T) {
	fs := afero.NewMemMapFs()
	there := filepath.Join("scratch", "settings.json")
	testsupport.WriteFileString(t, fs, there, "{}", 0o644)

	require.NoError(t, RequireDelivered(fs, SurfaceSettings, there),
		"a surface the session delivered presents cleanly")

	missing := filepath.Join("scratch", "gone.json")
	err := RequireDelivered(fs, SurfaceSettings, missing)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAbsentSharedSurface),
		"every absent surface refuses through one error, so a caller can tell this apart from a write failure")
	assert.Contains(t, err.Error(), missing, "the refusal names where it looked")
	assert.Contains(t, err.Error(), SurfaceSettings.String(), "and which surface it wanted")
}

// TestLaunchForm_String keeps the diagnostic names honest — they appear in the
// refusals above and in the wire enum's own naming.
func TestLaunchForm_String(t *testing.T) {
	assert.Equal(t, "deliver", LaunchFormDeliver.String())
	assert.Equal(t, "present", LaunchFormPresent.String())
	assert.Equal(t, "minimal", LaunchFormMinimal.String())
	assert.Equal(t, "LaunchForm(9)", LaunchForm(9).String())
}
