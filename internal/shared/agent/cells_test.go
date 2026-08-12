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

// dualRecordingDelivery is a recordingDelivery that ALSO carries the isolated
// path, the shape every real surface with a SharedRealization has (claude's
// context/MCP/settings). deliverOneShared only converts a resolved surface that
// carries DeliverIsolated, since the realization closure is a method value bound
// to that very instance — so a double meant to exercise the conversion has to
// carry it too.
type dualRecordingDelivery struct {
	recordingDelivery
}

func (dualRecordingDelivery) DeliverIsolated() (Delivered, error) { return stubHandle{}, nil }

// ---- compile-time guarantees ----------------------------------------------

// The type-level contracts the seam depends on. That these assignments COMPILE
// is the proof the stubs satisfy the interfaces.
var (
	_ Delivery  = recordingDelivery{}
	_ Delivery  = dualStub{}
	_ Delivered = stubHandle{}
)

// ---- isolated cells --------------------------------------------------------

// An isolated cell (worktree or container — both the SAME IsolatedCell type
// since a collapse merged the two behaviourally-identical wrapper types)
// accepts ANY Delivery and writes it into its own private dir — the
// compile-time signature (Deliver(Delivery)) encodes that a well-known write
// into a private dir is safe.
func TestIsolatedCells_DeliverAnyDeliveryIntoPrivateDir(t *testing.T) {
	for _, tc := range []struct {
		name string
		cell interface {
			Deliver(Delivery) (Delivered, error)
		}
		dir string
	}{
		{"worktree", NewIsolatedCell("/worktrees/agent-x"), "/worktrees/agent-x"},
		{"container", NewIsolatedCell("/home/agent"), "/home/agent"},
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
// SharedRealization returns (proving deliverOneShared prefers it over the
// well-known write); when nil, every (kind, approach) pair reports no
// realization, so deliverOneShared falls back to the loud well-known write.
// only, when non-nil, restricts realize to firing for exactly that approach —
// modeling claude's pair-keyed fork (system-prompt realizes, unsafe-file does
// not) without changing every OTHER test's "fires for whatever approach is
// queried" fake, which is what leaving only nil preserves.
type fakeSharedSet struct {
	realize func() (Delivered, error)
	only    *Approach
}

func (fakeSharedSet) Deliveries() []Delivery { return nil }
func (fakeSharedSet) SupportedApproaches(SurfaceKind) []Approach {
	return []Approach{ApproachUnsafeFile}
}
func (fakeSharedSet) DefaultApproach(SurfaceKind) (Approach, bool)       { return ApproachUnsafeFile, true }
func (fakeSharedSet) SurfaceFor(SurfaceKind, Approach) (Delivery, error) { return nil, nil }

func (f fakeSharedSet) SharedRealization(_ SurfaceKind, a Approach) (func() (Delivered, error), bool) {
	if f.realize == nil {
		return nil, false
	}
	if f.only != nil && *f.only != a {
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
	surface := dualRecordingDelivery{recordingDelivery{got: &wellKnownCalled, handle: stubHandle{}}}

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
	if _, err := NewIsolatedCell("/wt").Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell (worktree): %v", err)
	}
	if _, err := NewIsolatedCell("/home/agent").Deliver(dualStub{}); err != nil {
		t.Fatalf("isolated cell (container): %v", err)
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

// A kind-keyed SharedRealization must NOT be applied to a resolved surface that
// does not carry the isolated path. A kind can resolve to different deliveries
// per approach (claude's context: the dual-capable surface at UnsafeFile /
// SystemPrompt, a documented no-op at Hook), and the realization closure is a
// method value bound to the dual-capable instance — so converting the no-op
// would run a write the caller explicitly did not select, doubling the content
// the hook already carries.
func TestDeliverOneShared_SkipsRealizationForNonIsolatableSurface(t *testing.T) {
	resetStrictness(t)

	var realizeCalled bool
	var wellKnown deliveryCall
	realize := func() (Delivered, error) {
		realizeCalled = true
		return stubHandle{}, nil
	}
	r := &ResolvedSelection{set: fakeSharedSet{realize: realize}}
	// A plain Delivery — no DeliverIsolated, exactly like claude's Hook no-op.
	surface := recordingDelivery{got: &wellKnown, handle: nil, info: "engine/context"}

	d, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: ApproachHook, delivery: surface}, "/live")
	require.NoError(t, err)
	assert.Nil(t, d, "the no-op wrote nothing, so there is no cleanup handle")
	assert.False(t, realizeCalled, "the kind-keyed realization belongs to a DIFFERENT surface instance")
}

// EmptySurfaceSet.SurfaceFor must REFUSE every (kind, approach) pair. The
// SurfaceSet contract says SurfaceFor "errors on an unsupported combination the
// builder did not pre-validate", and an empty set supports nothing at all — so
// returning a nil Delivery with a nil error hands a direct caller an untyped-nil
// interface that panics on the first Deliver, with no error to branch on.
// claude's genuine no-op is a CONCRETE noopContextDelivery value, never a nil
// Delivery, so no backend depends on the (nil, nil) spelling.
func TestEmptySurfaceSet_SurfaceForErrorsForEveryKind(t *testing.T) {
	for _, k := range []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceCommands, SurfaceSkills} {
		for _, a := range []Approach{ApproachUnsafeFile, ApproachSystemPrompt, ApproachHook} {
			d, err := EmptySurfaceSet{}.SurfaceFor(k, a)
			require.Error(t, err, "empty set supports no (%s, %s) combination", k, a)
			assert.Nil(t, d, "a refused resolution must not also hand back a delivery")
			assert.Contains(t, err.Error(), k.String(), "the error must name the surface it refused")
		}
	}
}

// The acp/protocol-only path is UNAFFECTED by the refusal above: Build() never
// reaches SurfaceFor on an empty set, because SupportedApproaches is nil for
// every kind and Build treats an empty support list as a permitted no-op. This
// is the public-seam characterization pin — green before and after the fix —
// so the refusal cannot regress acp's "materialize nothing" contract.
func TestEmptySurfaceSet_BuildStillResolvesToNothing(t *testing.T) {
	everything, err := Select(EmptySurfaceSet{}).WithEverything().Build()
	require.NoError(t, err, "WithEverything over an empty set is a no-op, not an error")
	assert.Empty(t, everything.Deliveries())

	named, err := Select(EmptySurfaceSet{}).
		WithContext(ContextWriteUnsafeFile).
		WithMCP(MCPWriteUnsafeFile).
		WithSettings(SettingsWriteUnsafeFile).
		Build()
	require.NoError(t, err, "an explicitly named selection over an empty set is still a permitted no-op")
	assert.Empty(t, named.Deliveries())

	delivered, kinds, errs := everything.DeliverUnder(t.TempDir())
	assert.Empty(t, errs)
	assert.Empty(t, delivered)
	assert.Empty(t, kinds)
}

// RESOLUTION of U100-F05, half 1 of 2 (was:
// TestDeliverOneShared_RealizationWinsAtEveryApproach_U100F05, the anti-test
// pinning the kind-alone defect this pair-keyed re-key fixes — see the sibling
// test below for the other half). deliverOneShared now reads rs.approach:
// SharedRealization is keyed on the (kind, approach) PAIR, so a caller that
// explicitly named the out-of-cwd approach (ContextWriteSystemPrompt) gets the
// scratch conversion, race-safe and unwarned.
func TestDeliverOneShared_SystemPromptRealizes_U100F05(t *testing.T) {
	resetStrictness(t)

	var realizeCalled bool
	var wellKnown deliveryCall
	sysPrompt := ApproachSystemPrompt
	r := &ResolvedSelection{set: fakeSharedSet{only: &sysPrompt, realize: func() (Delivered, error) {
		realizeCalled = true
		return stubHandle{}, nil
	}}}
	surface := dualRecordingDelivery{recordingDelivery{got: &wellKnown, handle: stubHandle{}, info: "engine/context"}}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: ApproachSystemPrompt, delivery: surface}, "/live")
		require.NoError(t, err)
	})

	assert.True(t, realizeCalled, "system-prompt: the pair-keyed realization runs")
	assert.False(t, wellKnown.called, "system-prompt: the well-known write never runs when a realization exists")
	assert.NotContains(t, stderr, "warning:", "system-prompt: race-safe, no warning")
}

// RESOLUTION of U100-F05, half 2 of 2 (see the sibling test above). A caller
// that explicitly named the native-file approach (ContextWriteUnsafeFile —
// "write CLAUDE.md") gets EXACTLY that: no realization fires for this pair, so
// deliverOneShared falls to the well-known write, loudly warned — the
// honor-with-warning fork DECIDED for U100-F05 (a refuse-loudly alternative
// was rejected: the approach name itself is the acknowledgment).
func TestDeliverOneShared_UnsafeFileHonoredWithWarning_U100F05(t *testing.T) {
	resetStrictness(t)

	var realizeCalled bool
	var wellKnown deliveryCall
	sysPrompt := ApproachSystemPrompt
	r := &ResolvedSelection{set: fakeSharedSet{only: &sysPrompt, realize: func() (Delivered, error) {
		realizeCalled = true
		return stubHandle{}, nil
	}}}
	surface := dualRecordingDelivery{recordingDelivery{got: &wellKnown, handle: stubHandle{}, info: "engine/context"}}

	stderr := captureStderr(t, func() {
		_, err := r.deliverOneShared(resolvedSurface{kind: SurfaceContext, approach: ApproachUnsafeFile, delivery: surface}, "/live")
		require.NoError(t, err)
	})

	assert.False(t, realizeCalled, "unsafe-file: no realization for this pair — the caller's native-file request is honored, not silently converted")
	assert.True(t, wellKnown.called, "unsafe-file: the well-known write (the CLAUDE.md-equivalent write) runs")
	assert.Equal(t, "/live", wellKnown.dir, "the well-known write targets the live shared cwd")
	assert.Contains(t, stderr, "warning:", "unsafe-file into a shared cwd is loudly warned")
	assert.Contains(t, stderr, "engine/context", "the warning names the surface via UnsafeInfo")
}
