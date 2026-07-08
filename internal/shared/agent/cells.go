package agent

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
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

// SurfaceInputs is the shared, per-run superset of everything a backend's
// surfaces write: the assembled context (as a string for the ContextWriter-core
// engines, and the raw fragments for codex's file writer), the merged MCP config
// + profile/builtin bundle servers, the merged hook set + statusline policy, and
// the skill exports. Setup fills it once (from req + the merged lifecycle state)
// and hands it to a backend's CellDelivery.Build closure, which picks the fields
// IT needs and calls its own NewSurfaces. It is the cross-backend contract that
// lets the generic Setup build any backend's SurfaceSet without importing the
// concrete backend.
type SurfaceInputs struct {
	Context          string
	Fragments        []*Fragment
	MCP              *wire.MCPConfig
	BundleMCP        map[string]wire.MCPServer
	Hooks            *wire.HooksConfig
	ManageStatusline bool
	Skills           []CommandExport
}

// CellDelivery configures a launch backend's cell-based surface delivery. A
// backend that routes launch-time delivery through the typed-cell seam supplies
// one at InitLaunch; a backend that keeps the legacy lifecycle path (acp) passes
// nil, and Setup takes setupViaLifecycle instead.
type CellDelivery struct {
	// Build maps the shared per-run inputs to the backend's SurfaceSet, targeting
	// isolatedDir for any out-of-cwd (race-safe) surface — claude's append-flag /
	// --mcp-config / --settings scratch. Backends that write only well-known files
	// (codex/antigravity/kiro) ignore isolatedDir. For claude the closure also
	// stashes the concrete Surfaces on the backend so buildArgs can read each
	// out-of-cwd file's Path() after delivery.
	Build func(in SurfaceInputs, isolatedDir string) SurfaceSet

	// RawContext materializes the assembled context into the content-addressed
	// cache file (agent.WriteContextFile) as a Setup pre-step and sets the
	// CTXLOOM_CONTEXT_FILE env path — matching the legacy lifecycle path for the
	// file/hook engines (codex/antigravity/kiro). claude leaves it false: its
	// context rides an out-of-cwd launch flag or a well-known CLAUDE.md, never the
	// cache file.
	RawContext bool

	// ContextHook keys the SessionStart context-injection hook to the cache file's
	// hash (codex — the one engine that reads context via a hook that fires at run
	// time). antigravity/kiro divert context to AGENTS.md/steering (their context
	// surface), so their hook hash stays "". Requires RawContext (the hook reads
	// the cache file). claude leaves it false (context rides the append flag).
	ContextHook bool
}

// BuildWellKnown adapts a well-known-file backend's NewSurfaces into a
// CellDelivery.Build. Every surface writes its engine well-known path, so the
// isolated dir is ignored and the constructor runs with the default (OS)
// filesystem. It is the single source of truth for that adapter, shared by
// codex/antigravity/kiro — whose Build bodies would otherwise be identical
// boilerplate. (claude is the exception: it targets the isolated dir and stashes
// its concrete Surfaces for buildArgs, so it supplies its own Build.)
func BuildWellKnown[S SurfaceSet](newSurfaces func(SurfaceInputs, afero.Fs) S) func(SurfaceInputs, string) SurfaceSet {
	return func(in SurfaceInputs, _ string) SurfaceSet { return newSurfaces(in, nil) }
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

// UnsafeSurface is a Delivery that names ITSELF: UnsafeInfo returns a stable
// engine/surface identity (e.g. "claude/skills") for the loud warning the Unsafe
// hatch emits. A surface implements it so the escape hatch is SELF-DESCRIBING —
// the identity travels with the surface instead of being hand-typed at each wrap
// site. It is stable on purpose — a later gen-docs pass (plan S3) can enumerate
// every registered UnsafeSurface into a reference page of sanctioned unsafe
// deliveries.
type UnsafeSurface interface {
	Delivery
	// UnsafeInfo returns the surface's engine/surface identity (e.g. "claude/skills")
	// for the Unsafe warning.
	UnsafeInfo() string
}

// Unsafe adapts a self-describing Delivery into a RaceSafeDelivery so it can be
// handed to a SharedCell where isolation is genuinely unavoidable (the engine
// offers no isolated mechanism for this surface). It is the fail-loud escape
// hatch — the delivery analogue of --degraded: never silent, never the default.
// First preference is always to MAKE a surface race-safe via an engine flag;
// Unsafe is the documented last resort. dir is the shared cwd the well-known
// write lands in.
func Unsafe(s UnsafeSurface, dir string) RaceSafeDelivery {
	return unsafeDelivery{s: s, dir: dir}
}

// unsafeDelivery is the RaceSafeDelivery wrapper Unsafe returns.
type unsafeDelivery struct {
	s   UnsafeSurface
	dir string
}

// DeliverIsolated warns loudly, then performs the wrapped surface's well-known
// Delivery into the shared cwd (dir). An Unsafe delivery is a SANCTIONED,
// permitted action — the delivery analogue of --degraded — NOT a fatal fault, so
// it must never record a Finding the startup choke owner would abort on. It
// therefore routes ONE uniform, non-fatal warning through the warn primitive
// (agent.Warn → clidiag.Warn), which ALWAYS streams the family "<prog>: warning:"
// line to stderr and records nothing, in BOTH strict and degraded modes; the
// wrapped well-known Deliver then ALWAYS proceeds. The surface names itself via
// UnsafeInfo, so the warning needs no hand-typed reason.
func (u unsafeDelivery) DeliverIsolated() (Delivered, error) {
	Warn("unsafe: %s into shared cwd %s — no isolated mechanism; races concurrent agents", u.s.UnsafeInfo(), u.dir)
	return u.s.Deliver(u.dir)
}
