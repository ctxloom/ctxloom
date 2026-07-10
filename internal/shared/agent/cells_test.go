package agent

import (
	"io"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// deliveryCall records that a well-known Deliver ran and the dir it targeted.
type deliveryCall struct {
	called bool
	dir    string
}

// recordingDelivery is a plain Delivery: it records the dir passed to Deliver. It
// also self-describes via UnsafeInfo (info), so deliverOneShared's unsafeNamed
// fallback can pick up its identity for the loud warning when no SharedRealization
// exists for its kind.
type recordingDelivery struct {
	got    *deliveryCall
	handle Delivered
	info   string
}

func (s recordingDelivery) Deliver(dir string) (Delivered, error) {
	if s.got != nil {
		s.got.called = true
		s.got.dir = dir
	}
	return s.handle, nil
}

// UnsafeInfo returns the surface identity for the DeliverShared warning
// (deliverOneShared's unsafeNamed fallback).
func (s recordingDelivery) UnsafeInfo() string { return s.info }

// dualStub implements Delivery and additionally offers DeliverIsolated — the
// shape a SharedRealization closure wraps (e.g. claude context via
// --append-system-prompt-file), so every cell can deliver it and a
// SharedRealization can run its isolated path directly.
type dualStub struct{}

func (dualStub) Deliver(string) (Delivered, error)   { return stubHandle{}, nil }
func (dualStub) DeliverIsolated() (Delivered, error) { return stubHandle{}, nil }

// ---- compile-time guarantees ----------------------------------------------

// The type-level contracts the seam depends on. That these assignments COMPILE
// is the proof the stubs satisfy the interfaces.
var (
	_ Delivery  = recordingDelivery{}
	_ Delivery  = dualStub{}
	_ Delivered = stubHandle{}
)

// ---- isolated cells --------------------------------------------------------

// An isolated cell (worktree or container) accepts ANY Delivery and writes it
// into its own private dir — the compile-time signature (Deliver(Delivery))
// encodes that a well-known write into a private dir is safe.
func TestIsolatedCells_DeliverAnyDeliveryIntoPrivateDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell interface {
			Deliver(Delivery) (Delivered, error)
		}
		dir string
	}{
		{"worktree", NewDirectoryIsolatedCell("/worktrees/agent-x"), "/worktrees/agent-x"},
		{"container", NewProcessIsolatedCell("/home/agent"), "/home/agent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var call deliveryCall
			d, err := tc.cell.Deliver(recordingDelivery{got: &call, handle: stubHandle{}})
			require.NoError(t, err)
			require.NotNil(t, d)
			assert.True(t, call.called, "inner Deliver must run")
			assert.Equal(t, tc.dir, call.dir, "isolated cell writes into its private dir")
		})
	}
}

// ---- ResolvedSelection.deliverOneShared -------------------------------------

// fakeSharedSet is a minimal SurfaceSet double for exercising
// ResolvedSelection.deliverOneShared directly: realize, when set, is what
// SharedRealization returns for every kind (proving deliverOneShared prefers it
// over the well-known write); when nil, every kind reports no realization, so
// deliverOneShared falls back to the loud well-known write.
type fakeSharedSet struct {
	realize func() (Delivered, error)
}

func (fakeSharedSet) Deliveries() []Delivery                            { return nil }
func (fakeSharedSet) SupportedApproaches(SurfaceKind) []Approach         { return []Approach{ApproachUnsafeFile} }
func (fakeSharedSet) DefaultApproach(SurfaceKind) (Approach, bool)       { return ApproachUnsafeFile, true }
func (fakeSharedSet) SurfaceFor(SurfaceKind, Approach) (Delivery, error) { return nil, nil }

func (f fakeSharedSet) SharedRealization(SurfaceKind) (func() (Delivered, error), bool) {
	if f.realize == nil {
		return nil, false
	}
	return f.realize, true
}

var _ SurfaceSet = fakeSharedSet{}

// deliverOneShared prefers a backend's SharedRealization over the well-known
// Deliver: when SharedRealization exists for the surface's kind, its closure runs
// (and the well-known Deliver never does).
func TestDeliverOneShared_PrefersSharedRealization(t *testing.T) {
	var isolatedCalled bool
	var wellKnownCalled deliveryCall
	realize := func() (Delivered, error) {
		isolatedCalled = true
		return stubHandle{}, nil
	}
	r := &ResolvedSelection{set: fakeSharedSet{realize: realize}}
	surface := recordingDelivery{got: &wellKnownCalled, handle: stubHandle{}}

	d, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, delivery: surface}, "/live")
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.True(t, isolatedCalled, "SharedRealization's closure must run")
	assert.False(t, wellKnownCalled.called, "the well-known Deliver must NOT run when a realization exists")
}

// A dual-capable surface (Delivery, and additionally DeliverIsolated) is
// deliverable by every mechanism: isolated cells via its well-known Delivery, and
// deliverOneShared via its isolated path when the set advertises a
// SharedRealization for its kind.
func TestDualCapableSurface_WorksInEveryMechanism(t *testing.T) {
	if _, err := NewDirectoryIsolatedCell("/wt").Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell: %v", err)
	}
	if _, err := NewProcessIsolatedCell("/home/agent").Deliver(dualStub{}); err != nil {
		t.Fatalf("container cell: %v", err)
	}
	r := &ResolvedSelection{set: fakeSharedSet{realize: dualStub{}.DeliverIsolated}}
	if _, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, delivery: dualStub{}}, "/live"); err != nil {
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
	r := &ResolvedSelection{set: fakeSharedSet{}} // no realization for any kind

	var (
		d   Delivered
		err error
	)
	// (1) the WARN streams to stderr, naming the surface (via UnsafeInfo) and the
	// shared-cwd hazard.
	stderr := captureStderr(t, func() {
		d, err = r.deliverOneShared(resolvedSurface{kind: SurfaceSettings, delivery: surface}, dir)
	})
	require.NoError(t, err)
	require.NotNil(t, d)

	assert.Contains(t, stderr, "warning:", "the loud line uses the family warning prefix")
	assert.Contains(t, stderr, "engine/settings", "the warning names the surface via UnsafeInfo")
	assert.Contains(t, stderr, "shared cwd")

	// (2) the well-known Deliver ran (proceeded), targeting dir.
	assert.True(t, call.called, "the well-known Deliver must run — deliverOneShared proceeds, never aborts")
	assert.Equal(t, dir, call.dir, "well-known write must target the shared cwd dir")

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
	r := &ResolvedSelection{set: fakeSharedSet{}}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, delivery: surface}, "/w")
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "warning:", "the WARN still streams in degraded mode")
	assert.Contains(t, stderr, "engine/context", "the warning names the surface via UnsafeInfo")
	assert.True(t, call.called, "delivery still proceeds in degraded mode")
	assert.Equal(t, "/w", call.dir)
	assert.Empty(t, strictness.All(), "degraded mode records no finding (warn-and-continue)")
}
