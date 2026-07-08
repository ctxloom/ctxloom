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
// it. Unsafe's WARN streams through clidiag.Warn → os.Stderr, so this is how the
// tests observe the loud line without any recorded finding to inspect. (Package
// tests run sequentially unless they opt into t.Parallel, so the global swap is
// safe here.)
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

// recordingDelivery is a Delivery-ONLY surface (it does NOT implement
// RaceSafeDelivery): it records the dir passed to Deliver. Because it lacks the
// isolated mechanism, handing it to a SharedCell is a compile error — see the
// negative-case note below. It also self-describes via UnsafeInfo (info), so it is
// an UnsafeSurface the Unsafe hatch can wrap.
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

// UnsafeInfo returns the surface identity for the Unsafe warning.
func (s recordingDelivery) UnsafeInfo() string { return s.info }

// recordingRaceSafe is a RaceSafeDelivery surface: it records that its isolated
// path ran.
type recordingRaceSafe struct {
	isolatedCalled *bool
	handle         Delivered
}

func (s recordingRaceSafe) DeliverIsolated() (Delivered, error) {
	if s.isolatedCalled != nil {
		*s.isolatedCalled = true
	}
	return s.handle, nil
}

// dualStub implements BOTH Delivery and RaceSafeDelivery — a surface that has an
// isolated mechanism (e.g. claude context via --append-system-prompt-file), so
// every cell can deliver it.
type dualStub struct{}

func (dualStub) Deliver(string) (Delivered, error)   { return stubHandle{}, nil }
func (dualStub) DeliverIsolated() (Delivered, error) { return stubHandle{}, nil }

// ---- compile-time guarantees ----------------------------------------------

// The type-level contracts the seam depends on. That these assignments COMPILE
// is the proof the stubs (and Unsafe's wrapper) satisfy the interfaces.
var (
	_ Delivery         = recordingDelivery{}
	_ UnsafeSurface    = recordingDelivery{}
	_ RaceSafeDelivery = recordingRaceSafe{}
	_ Delivery         = dualStub{}
	_ RaceSafeDelivery = dualStub{}
	_ Delivered        = stubHandle{}
	// Unsafe's contract: its wrapper must be a RaceSafeDelivery so a SharedCell
	// accepts it.
	_ RaceSafeDelivery = unsafeDelivery{}
)

// Negative case — the compile-time guarantee, which cannot be asserted at
// runtime and so is documented here:
//
//	SharedCell{}.Deliver(recordingDelivery{})   // COMPILE ERROR
//
// recordingDelivery implements only Delivery, not RaceSafeDelivery, so it is NOT
// assignable to SharedCell.Deliver's RaceSafeDelivery parameter. A non-race-safe
// surface writing into the shared cwd is unrepresentable; the only way past the
// compiler is the explicit, warned Unsafe adapter.

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

// ---- shared cell -----------------------------------------------------------

// A SharedCell routes a race-safe surface through its isolated path — never the
// well-known Deliver.
func TestSharedCell_DeliversRaceSafeViaIsolatedPath(t *testing.T) {
	var isolated bool
	surface := recordingRaceSafe{isolatedCalled: &isolated, handle: stubHandle{}}

	d, err := SharedCell{}.Deliver(surface)
	require.NoError(t, err)
	require.NotNil(t, d)
	assert.True(t, isolated, "SharedCell must route through the isolated (race-safe) path")
}

// A dual-capable surface (implements both interfaces) is deliverable by EVERY
// cell: isolated cells via its well-known Delivery, the SharedCell via its
// race-safe path.
func TestDualCapableSurface_WorksInEveryCell(t *testing.T) {
	if _, err := NewDirectoryIsolatedCell("/wt").Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell: %v", err)
	}
	if _, err := NewProcessIsolatedCell("/home/agent").Deliver(dualStub{}); err != nil {
		t.Fatalf("container cell: %v", err)
	}
	if _, err := (SharedCell{}).Deliver(dualStub{}); err != nil {
		t.Fatalf("shared cell: %v", err)
	}
}

// ---- Unsafe escape hatch ---------------------------------------------------

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

// Unsafe wraps a Delivery-only surface into a RaceSafeDelivery. An Unsafe
// delivery is a SANCTIONED, permitted action — not a fatal fault — so
// DeliverIsolated must, in STRICT mode, (1) stream a WARN to stderr WITHOUT
// recording any finding (nothing for the startup choke owner to abort on), and
// (2) proceed with the inner well-known Deliver targeting reason.Dir. It warns
// AND proceeds; it does NOT abort startup.
func TestUnsafe_WarnsThenDeliversWellKnown(t *testing.T) {
	resetStrictness(t) // strict (non-degraded), clean findings

	var call deliveryCall
	dir := "/work/project"
	// The surface self-describes via UnsafeInfo — no hand-typed reason.
	surface := recordingDelivery{got: &call, handle: stubHandle{}, info: "engine/settings"}

	// rs is statically a RaceSafeDelivery — that IS Unsafe's declared return type,
	// the escape-hatch contract that lets a non-race-safe surface reach a
	// SharedCell (the wrapper's satisfaction is also pinned by the package-scope
	// `_ RaceSafeDelivery = unsafeDelivery{}` assertion above).
	rs := Unsafe(surface, dir)

	var (
		d   Delivered
		err error
	)
	// (1) the WARN streams to stderr, naming the surface (via UnsafeInfo) and the
	// shared-cwd hazard.
	stderr := captureStderr(t, func() {
		d, err = rs.DeliverIsolated()
	})
	require.NoError(t, err)
	require.NotNil(t, d)

	assert.Contains(t, stderr, "warning:", "the loud line uses the family warning prefix")
	assert.Contains(t, stderr, "engine/settings", "the warning names the surface via UnsafeInfo")
	assert.Contains(t, stderr, "shared cwd")

	// (2) the inner well-known Deliver ran (proceeded), targeting dir.
	assert.True(t, call.called, "inner Deliver must run — Unsafe proceeds, never aborts")
	assert.Equal(t, dir, call.dir, "well-known write must target the shared cwd dir")

	// A sanctioned Unsafe delivery records NO fatal finding, even in strict mode:
	// there is nothing for the startup choke owner to abort on.
	assert.Empty(t, strictness.All(), "Unsafe must not record a fatal finding in strict mode")
}

// In degraded mode the behavior is identical — the WARN still streams to stderr,
// no finding is recorded, and the well-known write still proceeds — because Unsafe
// is warn-and-proceed in BOTH modes.
func TestUnsafe_Degraded_DeliversWithoutRecording(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)

	var call deliveryCall
	rs := Unsafe(recordingDelivery{got: &call, handle: stubHandle{}, info: "engine/context"}, "/w")

	stderr := captureStderr(t, func() {
		_, err := rs.DeliverIsolated()
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "warning:", "the WARN still streams in degraded mode")
	assert.Contains(t, stderr, "engine/context", "the warning names the surface via UnsafeInfo")
	assert.True(t, call.called, "delivery still proceeds in degraded mode")
	assert.Equal(t, "/w", call.dir)
	assert.Empty(t, strictness.All(), "degraded mode records no finding (warn-and-continue)")
}
