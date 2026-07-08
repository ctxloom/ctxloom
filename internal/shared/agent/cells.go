package agent

import (
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// This file is the type-level FOUNDATION of ctxloom's unified surface-delivery
// seam: the two delivery interfaces distinguished by race-safety, the typed
// isolation cells that dispatch race-safety AT COMPILE TIME, and the Unsafe
// escape hatch. It is additive substrate — the existing runner-side delivery
// path (delivery.go, launch_backend.go) is untouched; a later cutover (plan
// Phase 2, delivery-factory-unification.plan.md) routes real delivery through
// these types.
//
// Model. A *surface* knows how to write itself to the engine's well-known path
// (Delivery), and OPTIONALLY how to write itself through an isolated per-run
// mechanism that cannot clobber a shared file (RaceSafeDelivery). A *cell*
// decides WHERE a surface lands and enforces the concurrency invariant BY TYPE:
// each cell's Deliver signature accepts only what it can safely deliver, so a
// racy surface↔cell pairing does not compile — the race is unrepresentable, not
// merely checked at runtime.
//
// (Delivered — the handle owning a delivery's cleanup — is defined in
// delivery.go and reused here.)

// Delivery writes one surface of a loadout to a well-known path under dir — the
// engine's native location (CLAUDE.md, .mcp.json, AGENTS.md, .claude/…). Every
// surface implements it. It returns a Delivered handle owning the cleanup that
// reverses the write.
type Delivery interface {
	// Deliver materializes the surface at its well-known location under dir and
	// returns a handle owning its cleanup.
	Deliver(dir string) (Delivered, error)
}

// RaceSafeDelivery writes one surface through an ISOLATED mechanism that cannot
// clobber a shared file — a per-run scratch file consumed via an engine flag,
// or a hook. A surface implements it ONLY when the engine offers such a
// mechanism; that capability (a genuine property of the surface, not a policy
// flag) is what lets it land in a SharedCell — the user's live cwd — without a
// race. It is an OPTIONAL companion to Delivery.
type RaceSafeDelivery interface {
	// DeliverIsolated materializes the surface through its isolated mechanism and
	// returns a handle owning its cleanup.
	DeliverIsolated() (Delivered, error)
}

// SurfaceSet is the per-backend set of delivery surfaces for one run, exposed so
// a cell can drive delivery without importing the concrete backend. Every
// backend's `Surfaces` value satisfies it. Deliveries feeds an ISOLATED cell
// (worktree/container/materialize) where a well-known write into a private dir
// is safe; SharedCwdDeliveries feeds a SHARED cell (the user's live cwd) at dir,
// returning each surface as a RaceSafeDelivery — genuinely race-safe where the
// engine offers an out-of-cwd flag, else wrapped in the loud agent.Unsafe adapter
// (each backend's method doc records which surfaces are which). This is additive
// substrate for the delivery cutover — no cell consumes it yet (plan S4b).
type SurfaceSet interface {
	// Deliveries returns every surface as a plain Delivery for an isolated cell.
	Deliveries() []Delivery
	// SharedCwdDeliveries returns every surface prepared for a SharedCell at dir,
	// each as a RaceSafeDelivery (assignable to SharedCell.Deliver).
	SharedCwdDeliveries(dir string) []RaceSafeDelivery
}

// CellKind is the resolved isolation cell a run executes in, decided host-side
// (mapped from the isolation.Policy) and carried to the plugin over the wire so
// Setup/buildArgs know which cell they run in. It is the plugin-side mirror of
// the grpc CellKind enum. The zero value is CellKindShared, matching the wire's
// UNSPECIFIED→Shared decode. It names the same three cells as the typed cell
// values above (SharedCell / DirectoryIsolatedCell / ProcessIsolatedCell).
type CellKind int

const (
	// CellKindShared is the user's live cwd — a shared directory (isolation None).
	CellKindShared CellKind = iota
	// CellKindDirectoryIsolated is a per-agent git worktree (isolation Worktree).
	CellKindDirectoryIsolated
	// CellKindProcessIsolated is a container (isolation Container, both tiers).
	CellKindProcessIsolated
)

// String renders the CellKind for diagnostics.
func (k CellKind) String() string {
	switch k {
	case CellKindDirectoryIsolated:
		return "directory-isolated"
	case CellKindProcessIsolated:
		return "process-isolated"
	default:
		return "shared"
	}
}

// isolatedCell is the shared base for cells that own a PRIVATE directory (a
// per-agent worktree, or a container's in-namespace filesystem). A well-known
// write into a private dir cannot race another session, so an isolated cell
// accepts ANY Delivery — the Deliver signature encodes that safety.
type isolatedCell struct {
	dir string
}

// Deliver writes the surface to this cell's private directory via the surface's
// well-known Delivery. Accepting a plain Delivery is safe precisely because the
// directory is private.
func (c isolatedCell) Deliver(s Delivery) (Delivered, error) {
	return s.Deliver(c.dir)
}

// DirectoryIsolatedCell is the isolated cell for a per-agent WORKTREE: dir is
// the private checkout, so Delivery lands the engine's native well-known files
// inside that checkout.
type DirectoryIsolatedCell struct {
	isolatedCell
}

// ProcessIsolatedCell is the isolated cell for a CONTAINER: dir is the
// filesystem-namespace location the co-located in-container engine reads. (The
// host-vs-guest resolution of dir from internal/lm/isolation is the seam wired
// later — plan S4; the container-mount question stays open there.)
type ProcessIsolatedCell struct {
	isolatedCell
}

// NewDirectoryIsolatedCell builds a worktree cell that writes surfaces into the
// private checkout dir.
func NewDirectoryIsolatedCell(dir string) DirectoryIsolatedCell {
	return DirectoryIsolatedCell{isolatedCell{dir: dir}}
}

// NewProcessIsolatedCell builds a container cell that writes surfaces into the
// in-namespace mount dir.
func NewProcessIsolatedCell(dir string) ProcessIsolatedCell {
	return ProcessIsolatedCell{isolatedCell{dir: dir}}
}

// SharedCell is the user's LIVE cwd — a shared directory other sessions may also
// use. Its Deliver accepts ONLY a RaceSafeDelivery: handing it a surface that is
// merely a Delivery is a COMPILE error, so a delivery that could clobber a
// shared file cannot be expressed. This is the race invariant enforced by the
// compiler rather than discovered at runtime.
type SharedCell struct{}

// Deliver writes the surface via its isolated, race-free mechanism. The
// signature — RaceSafeDelivery, not Delivery — IS the guarantee: only a
// self-isolating surface reaches the shared cwd.
func (SharedCell) Deliver(s RaceSafeDelivery) (Delivered, error) {
	return s.DeliverIsolated()
}

// UnsafeReason is the structured, stable record of ONE sanctioned unsafe
// delivery: which Surface, Why isolation is genuinely unavoidable, the Dir the
// well-known write lands in, and the strictness Class of the emitted warning.
// It is stable on purpose — a later gen-docs pass (plan S3) enumerates every
// registered UnsafeReason into a reference page of sanctioned unsafe deliveries.
type UnsafeReason struct {
	Surface string
	Why     string
	Dir     string
	Class   strictness.Class
}

// Unsafe adapts a plain Delivery into a RaceSafeDelivery so it can be handed to
// a SharedCell where isolation is genuinely unavoidable (the engine offers no
// isolated mechanism for this surface). It is the fail-loud escape hatch — the
// delivery analogue of --degraded: never silent, never the default. First
// preference is always to MAKE a surface race-safe via an engine flag; Unsafe is
// the documented last resort.
func Unsafe(s Delivery, r UnsafeReason) RaceSafeDelivery {
	return unsafeDelivery{s: s, r: r}
}

// unsafeDelivery is the RaceSafeDelivery wrapper Unsafe returns.
type unsafeDelivery struct {
	s Delivery
	r UnsafeReason
}

// DeliverIsolated warns loudly, then performs the wrapped surface's well-known
// Delivery into the shared cwd (r.Dir). An Unsafe delivery is a SANCTIONED,
// permitted action — the delivery analogue of --degraded — NOT a fatal fault, so
// it must never record a Finding the startup choke owner would abort on. It
// therefore routes the warning through the non-fatal warn primitive (agent.Warn →
// clidiag.Warn), which ALWAYS streams the family "<prog>: warning:" line to
// stderr and records nothing, in BOTH strict and degraded modes; the wrapped
// well-known Deliver then ALWAYS proceeds. The structured UnsafeReason (incl.
// r.Class) is retained for the gen-docs pass (plan S3) that enumerates every
// sanctioned unsafe delivery into a reference page — the class classifies the
// exception for docs, it no longer gates startup.
func (u unsafeDelivery) DeliverIsolated() (Delivered, error) {
	Warn("unsafe delivery of %s into a shared cwd: %s", u.r.Surface, u.r.Why)
	return u.s.Deliver(u.r.Dir)
}

// UnsafeApply wraps a plain Delivery as a RaceSafeDelivery for a shared cwd,
// classified strictness.ClassApply — the hook/config APPLY class every engine's
// well-known surface writes fall under. It is the shared shorthand a backend
// reaches for when its engine offers NO out-of-cwd flag for a surface, so the
// only way that surface reaches a SharedCell is the loud Unsafe hatch. surface,
// why, and dir fill the sanctioned-unsafe UnsafeReason a gen-docs pass (plan S3)
// enumerates. It exists so the identical per-backend Unsafe(...) helper no longer
// has to be mirrored in every engine package.
func UnsafeApply(d Delivery, surface, why, dir string) RaceSafeDelivery {
	return Unsafe(d, UnsafeReason{
		Surface: surface,
		Why:     why,
		Dir:     dir,
		Class:   strictness.ClassApply,
	})
}
