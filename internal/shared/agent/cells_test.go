package agent

import (
	"io"
	"os"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent/present"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// captureStderr redirects os.Stderr around fn and returns everything written to
// it. DeliverShared's no-realization fallback WARN streams through clidiag.Warn →
// os.Stderr, so this is how the tests observe the loud line without any recorded
// finding to inspect. (Package tests run sequentially unless they opt into
// t.Parallel, so the global swap is safe here.)
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stderr
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

// ---- stubs ----------------------------------------------------------------

// stubHandle is a no-op Delivered.
type stubHandle struct{}

func (stubHandle) Cleanup() error { return nil }

// deliveryCall records that a well-known Deliver ran and the advised roots it
// was handed.
type deliveryCall struct {
	called bool
	start  present.Start
}

// projectDir is the host side of the project root the recorded Start carries —
// where a well-known write lands.
func (c deliveryCall) projectDir() string { return c.start.Paths().ProjectRoot.Host }

// recordingDelivery is a plain Approach: it records the Start passed to
// Deliver and presents nothing in particular. It also self-describes via
// UnsafeInfo (info), so deliverOneShared's unsafeNamed fallback can pick up
// its identity for the loud warning when it has no OutOfCwd form.
type recordingDelivery struct {
	got    *deliveryCall
	handle Delivered
	info   string
}

func (s recordingDelivery) Present(start present.Start) present.Presentation {
	return start.UnderProjectRoot("x").Build()
}

func (s recordingDelivery) Deliver(start present.Start) (Delivered, error) {
	if s.got != nil {
		s.got.called = true
		s.got.start = start
	}
	return s.handle, nil
}

// UnsafeInfo returns the surface identity for the DeliverShared warning
// (deliverOneShared's unsafeNamed fallback).
func (s recordingDelivery) UnsafeInfo() string { return s.info }

// dualStub implements Approach and additionally offers DeliverIsolated — the
// OutOfCwd shape (e.g. claude context via --append-system-prompt-file), so
// every cell can deliver it and a shared-cwd delivery runs its isolated form.
type dualStub struct{}

func (dualStub) Present(start present.Start) present.Presentation {
	return start.UnderScratch("x").Build()
}
func (dualStub) Deliver(present.Start) (Delivered, error)         { return stubHandle{}, nil }
func (dualStub) DeliverIsolated(present.Start) (Delivered, error) { return stubHandle{}, nil }

// dualRecordingDelivery is a recordingDelivery that ALSO carries the isolated
// form and records whether it ran — the shape every real approach with an
// out-of-cwd form has (claude's system-prompt context, MCP, settings).
type dualRecordingDelivery struct {
	recordingDelivery
	isolated *bool
}

func (d dualRecordingDelivery) DeliverIsolated(present.Start) (Delivered, error) {
	if d.isolated != nil {
		*d.isolated = true
	}
	return stubHandle{}, nil
}

// riderStub is a Rider: it writes nothing (nil handle) and rides settings,
// the shape of hook-carried context.
type riderStub struct{ got *deliveryCall }

func (riderStub) Present(start present.Start) present.Presentation { return present.Presentation{} }
func (riderStub) Rides() SurfaceKind                               { return SurfaceSettings }
func (r riderStub) Deliver(start present.Start) (Delivered, error) {
	if r.got != nil {
		r.got.called = true
		r.got.start = start
	}
	return nil, nil
}

// ---- compile-time guarantees ----------------------------------------------

// The type-level contracts the seam depends on. That these assignments COMPILE
// is the proof the stubs satisfy the interfaces.
var (
	_ Approach  = recordingDelivery{}
	_ Approach  = dualStub{}
	_ OutOfCwd  = dualStub{}
	_ Rider     = riderStub{}
	_ Delivered = stubHandle{}
)

// ---- isolated cells --------------------------------------------------------

// An isolated cell (worktree or container — both the SAME IsolatedCell type
// since a collapse merged the two behaviourally-identical wrapper types)
// accepts ANY Delivery and hands it the ADVISED ROOTS the cell was built from
// — the surface never sees a bare dir string, so it cannot compute a location
// of its own; it writes under the project root the pre-advice settled.
func TestIsolatedCells_DeliverHandsTheAdvisedRootsToAnyDelivery(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell interface {
			Deliver(Delivery) (Delivered, error)
		}
		dir string
	}{
		{"worktree", NewIsolatedCell(present.ProjectOnHost("/worktrees/agent-x")), "/worktrees/agent-x"},
		{"container", NewIsolatedCell(present.ProjectOnHost("/home/agent")), "/home/agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var call deliveryCall
			d, err := tc.cell.Deliver(recordingDelivery{got: &call, handle: stubHandle{}})
			require.NoError(t, err)
			require.NotNil(t, d)
			assert.True(t, call.called, "inner Deliver must run")
			assert.Equal(t, tc.dir, call.projectDir(), "isolated cell hands the surface its private dir as the advised project root")
		})
	}
}

// A Start whose project root was never resolved is refused at the seam, never
// handed on: a "" root joined into a well-known path yields a BARE RELATIVE
// path that looks well-formed and lands wherever the process happens to be
// — the failure the open-sets ruling names as the dangerous one. Both entry
// points into a Delivery (the isolated cell and the shared-cwd delivery) share
// the guard.
func TestDelivery_UnrootedStartIsRefusedAtBothEntryPoints(t *testing.T) {
	unrooted := present.New(present.OnHost(present.Paths{}))

	t.Run("isolated cell", func(t *testing.T) {
		var call deliveryCall
		_, err := NewIsolatedCell(unrooted).Deliver(recordingDelivery{got: &call, handle: stubHandle{}})
		require.ErrorIs(t, err, ErrUnrootedDelivery)
		assert.False(t, call.called, "the surface must not run against an unresolved root")
	})

	t.Run("shared cwd", func(t *testing.T) {
		var call deliveryCall
		r := &ResolvedSelection{}
		rs := resolvedSurface{kind: SurfaceCommands, name: ApproachUnsafeFile,
			approach: recordingDelivery{got: &call, handle: stubHandle{}, info: "x"}}
		_, err := r.deliverOneShared(rs, unrooted)
		require.ErrorIs(t, err, ErrUnrootedDelivery)
		assert.False(t, call.called, "the surface must not run against an unresolved root")
	})
}

// ---- ResolvedSelection.deliverOneShared -------------------------------------

// deliverOneShared prefers an approach's own OutOfCwd form over its well-known
// Deliver: when the approach carries DeliverIsolated, that form runs (and the
// well-known Deliver never does).
func TestDeliverOneShared_PrefersOutOfCwdForm(t *testing.T) {
	var isolatedCalled bool
	var wellKnownCalled deliveryCall
	r := &ResolvedSelection{}
	surface := dualRecordingDelivery{recordingDelivery{got: &wellKnownCalled, handle: stubHandle{}}, &isolatedCalled}

	d, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: surface}, present.ProjectOnHost("/live"))
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.True(t, isolatedCalled, "the out-of-cwd form must run")
	assert.False(t, wellKnownCalled.called, "the well-known Deliver must NOT run when an out-of-cwd form exists")
}

// A dual-capable approach (Deliver, and additionally DeliverIsolated) is
// deliverable by every mechanism: isolated cells via its well-known Delivery,
// and deliverOneShared via its isolated form.
func TestDualCapableSurface_WorksInEveryMechanism(t *testing.T) {
	if _, err := NewIsolatedCell(present.ProjectOnHost("/wt")).Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell (worktree): %v", err)
	}
	if _, err := NewIsolatedCell(present.ProjectOnHost("/home/agent")).Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell (container): %v", err)
	}
	r := &ResolvedSelection{}
	if _, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: dualStub{}}, present.ProjectOnHost("/live")); err != nil {
		t.Fatalf("deliverOneShared: %v", err)
	}
}

// ---- deliverOneShared: no-realization fallback ------------------------------

// resetStrictness restores pristine strict-mode state around a test and
// registers cleanup so the package-global finding collector never bleeds.
func resetStrictness(t *testing.T) {
	t.Helper()
	strictness.Reset()
	strictness.SetDegraded(false)
	t.Cleanup(func() {
		strictness.Reset()
		strictness.SetDegraded(false)
	})
}

// When the backend offers NO SharedRealization for a surface's kind,
// deliverOneShared falls back to the well-known write — a SANCTIONED, permitted
// action, not a fatal fault — so it must, in STRICT mode, (1) stream a WARN to
// stderr WITHOUT recording any finding (nothing for the startup choke owner to
// abort on), and (2) proceed with the well-known Deliver targeting dir. It warns
// AND proceeds; it does NOT abort startup.
func TestDeliverOneShared_NoRealization_WarnsThenProceeds(t *testing.T) {
	resetStrictness(t) // strict (non-degraded), clean findings

	var call deliveryCall
	dir := "/work/project"
	// The surface self-describes via UnsafeInfo — no hand-typed reason.
	surface := recordingDelivery{got: &call, handle: stubHandle{}, info: "engine/settings"}
	r := &ResolvedSelection{} // the approach has no out-of-cwd form

	var (
		d   Delivered
		err error
	)
	// (1) the WARN streams to stderr, naming the surface (via UnsafeInfo) and the
	// shared-cwd hazard.
	stderr := captureStderr(t, func() {
		d, err = r.deliverOneShared(resolvedSurface{kind: SurfaceSettings, approach: surface}, present.ProjectOnHost(dir))
	})
	require.NoError(t, err)
	require.NotNil(t, d)

	assert.Contains(t, stderr, "warning:", "the loud line uses the family warning prefix")
	assert.Contains(t, stderr, "engine/settings", "the warning names the surface via UnsafeInfo")
	assert.Contains(t, stderr, "shared cwd")

	// (2) the well-known Deliver ran (proceeded), targeting dir.
	assert.True(t, call.called, "the well-known Deliver must run — deliverOneShared proceeds, never aborts")
	assert.Equal(t, dir, call.projectDir(), "well-known write must target the shared cwd dir")

	// A sanctioned fallback records NO fatal finding, even in strict mode: there
	// is nothing for the startup choke owner to abort on.
	assert.Empty(t, strictness.All(), "the fallback must not record a fatal finding in strict mode")
}

// In degraded mode the behavior is identical — the WARN still streams to stderr,
// no finding is recorded, and the well-known write still proceeds — because the
// fallback is warn-and-proceed in BOTH modes.
func TestDeliverOneShared_Degraded_WarnsWithoutRecording(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)

	var call deliveryCall
	surface := recordingDelivery{got: &call, handle: stubHandle{}, info: "engine/context"}
	r := &ResolvedSelection{}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: surface}, present.ProjectOnHost("/w"))
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "warning:", "the WARN still streams in degraded mode")
	assert.Contains(t, stderr, "engine/context", "the warning names the surface via UnsafeInfo")
	assert.True(t, call.called, "delivery still proceeds in degraded mode")
	assert.Equal(t, "/w", call.projectDir())
	assert.Empty(t, strictness.All(), "degraded mode records no finding (warn-and-continue)")
}

// A Rider (hook-carried context) writes nothing of its own: deliverOneShared
// runs its no-op Deliver, records no handle, and emits NO warning — there is
// no well-known write into the shared cwd to warn about, and converting it
// to some other surface's out-of-cwd form would run a write the caller never
// selected, doubling the content the hook already carries.
func TestDeliverOneShared_RiderIsANoOpWithoutWarning(t *testing.T) {
	resetStrictness(t)

	var call deliveryCall
	r := &ResolvedSelection{}
	var (
		d   Delivered
		err error
	)
	stderr := captureStderr(t, func() {
		d, err = r.deliverOneShared(resolvedSurface{kind: SurfaceContext, name: ApproachHook, approach: riderStub{got: &call}}, present.ProjectOnHost("/live"))
	})
	require.NoError(t, err)
	assert.Nil(t, d, "the no-op wrote nothing, so there is no cleanup handle")
	assert.True(t, call.called, "the rider's own Deliver runs (it is what records the no-op)")
	assert.NotContains(t, stderr, "warning:", "a rider writes nothing into the shared cwd, so nothing is warned")
}

// An EMPTY Declaration (a protocol-only engine that materializes no files)
// resolves to nothing rather than erroring: WithEverything selects nothing,
// and an explicitly named selection over it is still a permitted no-op — a
// kind the engine does not declare is a fold, not a fault. This is what keeps
// the "materialize nothing" contract from regressing.
func TestEmptyDeclaration_BuildResolvesToNothing(t *testing.T) {
	everything, err := Select(Declaration{}).WithEverything().Build(SurfaceInputs{}, nil)
	require.NoError(t, err, "WithEverything over an empty declaration is a no-op, not an error")
	assert.Empty(t, everything.Deliveries())

	named, err := Select(Declaration{}).
		With(SurfaceContext, ApproachUnsafeFile).
		With(SurfaceMCP, ApproachUnsafeFile).
		With(SurfaceSettings, ApproachUnsafeFile).
		Build(SurfaceInputs{}, nil)
	require.NoError(t, err, "an explicitly named selection over an empty declaration is still a permitted no-op")
	assert.Empty(t, named.Deliveries())

	delivered, kinds, errs := everything.DeliverUnder(present.ProjectOnHost(t.TempDir()))
	assert.Empty(t, errs)
	assert.Empty(t, delivered)
	assert.Empty(t, kinds)
}

// A name the engine does NOT declare for a kind it DOES declare is a loud
// Build error naming the surface, the name and the declared set — never a
// zero value that delivers nothing while reporting success, and never a
// silent fallback to the default. This is the open-set's price (no compiler
// check) paid the way the open-sets ruling requires: the lookup fails loud.
func TestBuild_UndeclaredNameIsLoudError(t *testing.T) {
	decl := Declaration{
		SurfaceContext: Presents("eng", SurfaceContext, ApproachUnsafeFile, func(SurfaceInputs, afero.Fs) Approach {
			return recordingDelivery{handle: stubHandle{}}
		}),
	}
	_, err := Select(decl).With(SurfaceContext, "steering").Build(SurfaceInputs{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "context")
	assert.Contains(t, err.Error(), `"steering"`)
	assert.Contains(t, err.Error(), ApproachUnsafeFile, "the error names what IS declared")
}

// A Rider selected without the surface it rides is refused at Build: delivered
// alone it would write nothing and report success.
func TestBuild_RiderWithoutRiddenKindIsRefused(t *testing.T) {
	decl := Declaration{
		SurfaceContext: Presents("eng", SurfaceContext, ApproachHook, func(SurfaceInputs, afero.Fs) Approach {
			return riderStub{}
		}),
		SurfaceSettings: Presents("eng", SurfaceSettings, ApproachUnsafeFile, func(SurfaceInputs, afero.Fs) Approach {
			return recordingDelivery{handle: stubHandle{}}
		}),
	}
	_, err := Select(decl).With(SurfaceContext, ApproachHook).Build(SurfaceInputs{}, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "settings")

	_, err = Select(decl).With(SurfaceContext, ApproachHook).With(SurfaceSettings, ApproachUnsafeFile).Build(SurfaceInputs{}, nil)
	require.NoError(t, err)
}

// A LaunchOnly approach is refused at rest: its bytes are announced by a
// launch flag, and DeliverUnder has no argv sink to hand that flag to.
func TestDeliverUnder_LaunchOnlyIsRefused(t *testing.T) {
	var call deliveryCall
	r := &ResolvedSelection{surfaces: []resolvedSurface{
		{kind: SurfaceContext, name: "system-prompt", approach: launchOnlyStub{recordingDelivery{got: &call, handle: stubHandle{}}}},
	}}
	_, kinds, errs := r.DeliverUnder(present.ProjectOnHost("/at-rest"))
	require.Len(t, errs, 1)
	assert.Contains(t, errs[0].Error(), "system-prompt")
	assert.Contains(t, errs[0].Error(), "no argv sink")
	assert.Empty(t, kinds)
	assert.False(t, call.called, "the write must not run at rest")
}

// launchOnlyStub marks recordingDelivery as LaunchOnly.
type launchOnlyStub struct{ recordingDelivery }

func (launchOnlyStub) LaunchOnly() {}

// A caller that explicitly named the out-of-cwd approach (claude's
// system-prompt: an approach WITH an OutOfCwd form) gets the scratch
// conversion, race-safe and unwarned.
func TestDeliverOneShared_OutOfCwdApproachRealizesUnwarned(t *testing.T) {
	resetStrictness(t)

	var realizeCalled bool
	var wellKnown deliveryCall
	r := &ResolvedSelection{}
	surface := dualRecordingDelivery{recordingDelivery{got: &wellKnown, handle: stubHandle{}, info: "engine/context"}, &realizeCalled}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, name: "system-prompt", approach: surface}, present.ProjectOnHost("/live"))
		require.NoError(t, err)
	})

	assert.True(t, realizeCalled, "the out-of-cwd form runs")
	assert.False(t, wellKnown.called, "the well-known write never runs when an out-of-cwd form exists")
	assert.NotContains(t, stderr, "warning:", "race-safe, no warning")
}

// A caller that explicitly named the native-file approach (an approach
// WITHOUT an OutOfCwd form — "write CLAUDE.md") gets EXACTLY that: the
// well-known write, loudly warned — the honor-with-warning fork (a
// refuse-loudly alternative was rejected: the approach name itself is the
// acknowledgment). Because the two are DIFFERENT approach values, nothing
// can convert the native-file one by mistake.
func TestDeliverOneShared_UnsafeFileHonoredWithWarning(t *testing.T) {
	resetStrictness(t)

	var wellKnown deliveryCall
	r := &ResolvedSelection{}
	surface := recordingDelivery{got: &wellKnown, handle: stubHandle{}, info: "engine/context"}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, name: ApproachUnsafeFile, approach: surface}, present.ProjectOnHost("/live"))
		require.NoError(t, err)
	})

	assert.True(t, wellKnown.called, "unsafe-file: the well-known write (the CLAUDE.md-equivalent write) runs")
	assert.Equal(t, "/live", wellKnown.projectDir(), "the well-known write targets the live shared cwd")
	assert.Contains(t, stderr, "warning:", "unsafe-file into a shared cwd is loudly warned")
	assert.Contains(t, stderr, "engine/context", "the warning names the surface via UnsafeInfo")
}
