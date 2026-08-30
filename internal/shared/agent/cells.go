package agent

import (
	"fmt"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// This file is the type-level FOUNDATION of ctxloom's unified surface-delivery
// seam: the Delivery interface every surface implements, the typed isolation
// cells that land a well-known write in a private dir, and the approach-keyed
// SurfaceSelection builder that resolves a caller's named
// per-surface approach to a concrete Delivery via each backend's per-provider
// dispatch. Race-safety is handled by the CELL, not a parallel type hierarchy:
// an isolated cell's private dir makes any well-known write race-free by
// construction, and a SHARED-cwd delivery either runs the backend's
// SharedRealization (claude's out-of-cwd scratch conversion) or performs the
// well-known write with a loud warning — the caller's UNSAFE_FILE approach
// choice IS that warning's acknowledgment.
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

// SurfaceKind names the CROSS-BACKEND category a delivery surface belongs to —
// the union over every engine's surfaces. It exists for exactly two purposes:
// (1) the opt-in SurfaceSelection builder (a caller states which kinds it
// delivers), and (2) reporting which surfaces a delivery actually wrote. It is
// deliberately NOT a dispatch key — no code branches on a surface's kind to
// decide HOW to write it (that stays each surface's own Deliver). Engines fold
// several concerns into one file (claude/kiro settings carry hooks; codex config
// carries hooks + MCP) — those all map
// to SurfaceSettings, so a caller selecting Settings gets the whole folded file.
type SurfaceKind int

const (
	// SurfaceContext is the engine's context surface (CLAUDE.md, .agents/AGENTS.md,
	// steering, or codex's context cache file).
	SurfaceContext SurfaceKind = iota
	// SurfaceMCP is the engine's MCP server config (.mcp.json, mcp_config.json,
	// .kiro/settings/mcp.json). codex folds MCP into its config surface (Settings).
	SurfaceMCP
	// SurfaceSettings is the engine's settings/hooks surface (.claude/settings.json,
	// codex config.toml, .agents/hooks.json, kiro agent JSON).
	SurfaceSettings
	// SurfaceCommands is the engine's slash-command files (a skill-only
	// engine's writer renders these to its native SKILL.md convention — see
	// RenderCommandAsSkillFile — but the export a caller hands this surface is
	// always a command, never a skill; SurfaceSkills is the discrete kind for
	// genuine Agent Skill packages).
	SurfaceCommands
	// SurfaceSkills is the engine's Agent Skills surface — SKILL.md package
	// directories (claude .claude/skills/, kiro .kiro/skills/, …). Distinct
	// from SurfaceCommands: a command is a user-invoked slash-command template,
	// a skill is a model-invoked capability package (SKILL.md + optional
	// scripts/assets), loaded by the engine via progressive disclosure. See
	// the skill/command split plan (skill-command-split.plan.md §3.4).
	SurfaceSkills
)

// String renders the kind as the stable lowercase label used in delivery reports.
func (k SurfaceKind) String() string {
	switch k {
	case SurfaceContext:
		return "context"
	case SurfaceMCP:
		return "mcp"
	case SurfaceSettings:
		return "settings"
	case SurfaceCommands:
		return "commands"
	case SurfaceSkills:
		return "skills"
	default:
		return "unknown"
	}
}

// ParseSurfaceKind is SurfaceKind.String's inverse, and lives beside it so the
// two cannot drift: a kind renamed for a user's eyes is renamed for their
// keyboard in the same edit. It exists because the vocabulary is now typed by
// humans (`profile materialize --surface context=unsafe-file`), not only
// rendered to them.
//
// An unrecognised name is an ERROR rather than a zero value. SurfaceContext is
// iota 0, so returning the zero value on a typo would silently retarget an
// override at the context surface — the one kind whose delivery a user is most
// likely to be overriding on purpose.
func ParseSurfaceKind(s string) (SurfaceKind, error) {
	for _, k := range surfaceOrder {
		if k.String() == s {
			return k, nil
		}
	}
	return 0, fmt.Errorf("unknown surface kind %q (known: %s)", s, strings.Join(SurfaceKindNames(), ", "))
}

// SurfaceKindNames lists every kind's label, in delivery order. It reads
// surfaceOrder — the one enumeration — so error text, --help and shell
// completion cannot fall out of step with the enum or with each other. A kind
// added to surfaceOrder appears in all three without touching them.
func SurfaceKindNames() []string {
	names := make([]string, 0, len(surfaceOrder))
	for _, k := range surfaceOrder {
		names = append(names, k.String())
	}
	return names
}

// KindedDelivery is a Delivery that knows its SurfaceKind, so the SurfaceSelection
// builder can opt kinds in without a per-backend type switch (it asks each surface
// its kind). Every concrete backend surface implements it.
type KindedDelivery interface {
	Delivery
	// Kind reports which cross-backend surface category this delivery is.
	Kind() SurfaceKind
}

// SurfaceSet is the per-backend set of delivery surfaces for one run, exposed so
// the SurfaceSelection builder can drive delivery without importing the concrete
// backend. Every backend's `Surfaces` value satisfies it. The four
// approach-dispatch methods below are the per-provider dispatch
// the opt-in SurfaceSelection builder resolves a caller's named approach through
// — descriptor-keyed, never a cross-backend type switch.
//
// There is deliberately NO Deliveries() here. Iterating a set's surfaces is what
// Select(set).WithEverything().Build().Deliveries() does, approach-resolved and
// with the kind attached. A raw, unresolved Deliveries() on this interface would
// be a SECOND iteration path — one five backends hand-maintain as a slice
// literal and no production caller reads — for a materialized tree measured to
// be identical (see TestDeliveries_ResolvedSelectionMaterializesEverySurface in
// internal/lm/backends).
type SurfaceSet interface {
	// SupportedApproaches reports the delivery approaches this backend offers for
	// kind. An EMPTY result means the backend has no distinct surface of that kind
	// (codex folds MCP into its config/settings surface) — selecting the kind is
	// then a permitted no-op rather than an error. A non-empty result that omits a
	// named approach makes that (kind, approach) combination unsupported — the
	// builder's Build() rejects it loudly (WithContext(Hook) on kiro,
	// WithContext(SystemPrompt) on codex).
	SupportedApproaches(kind SurfaceKind) []Approach
	// DefaultApproach reports the approach WithEverything selects for kind — the
	// backend's native realization. false means kind is absent/folded for this
	// backend (codex's MCP), so WithEverything skips it entirely.
	DefaultApproach(kind SurfaceKind) (Approach, bool)
	// SurfaceFor resolves one (kind, approach) to the concrete Delivery that
	// executes it, against the SAME underlying surface instance NewSurfaces built
	// (never a second construction) — so any state a prior delivery recorded (e.g.
	// claude's out-of-cwd scratch Path()) stays visible to a later reader. It
	// errors on an unsupported combination the builder did not pre-validate.
	SurfaceFor(kind SurfaceKind, a Approach) (Delivery, error)
	// SharedRealization reports the backend's out-of-cwd, genuinely race-safe
	// conversion for the (kind, approach) pair — claude's
	// --append-system-prompt-file /--mcp-config / --settings scratch writers.
	// false means the backend has no such conversion for this pair (every kind
	// on codex/kiro; claude's commands; claude's context at ApproachUnsafeFile,
	// which the caller explicitly asked to write natively), so a SHARED-cwd
	// delivery falls back to the loud well-known write. The returned closure,
	// when non-nil, performs the isolated write and returns its handle.
	SharedRealization(kind SurfaceKind, a Approach) (func() (Delivered, error), bool)
}

// SurfaceInputs is the shared, per-run superset of everything a backend's
// surfaces write: the assembled context (as a string for the ContextWriter-core
// engines, and the raw fragments for codex's file writer), the merged MCP config
// + profile/builtin bundle servers, the merged hook set + statusline policy, and
// the command exports. Setup fills it once (from req + the merged lifecycle state)
// and hands it to a backend's CellDelivery.Build closure, which picks the fields
// IT needs and calls its own NewSurfaces. It is the cross-backend contract that
// lets the generic Setup build any backend's SurfaceSet without importing the
// concrete backend.
type SurfaceInputs struct {
	Context          string
	Fragments        []*Fragment
	BundleMCP        map[string]wire.MCPServer
	Hooks            *wire.HooksConfig
	ManageStatusline bool
	Commands         []CommandExport
	// SelfContainedCommands, when true, tells a backend's commands surface to
	// materialize commands WITHOUT deduping against the delivering machine's own
	// skill/command directories (e.g. claude's ~/.claude/commands) — for portable
	// `profile materialize --target` artifacts whose launch environment is not
	// this host. Live launch, `manage hooks install` apply, and container
	// delivery all share their process/home with the launch environment, so they
	// leave this false (the default) and keep self-resolving dedup ON.
	SelfContainedCommands bool
	// Skills carries the per-target-agent Agent Skill package exports — the
	// skills surface's analog of Commands. Each SkillExport is a whole package
	// (SKILL.md + optional sibling files), not a single file.
	Skills []SkillExport
	// SelfContainedSkills mirrors SelfContainedCommands for the skills surface:
	// true for a portable `profile materialize --target` artifact, false (the
	// default) for a live/apply/container delivery that shares this host.
	SelfContainedSkills bool
	// MCPCommandOverride, when non-empty, replaces CtxloomCommand() as the
	// ctxloom-managed MCP server's stdio command (see ResolveMCPCommand).
	// setupViaCells fills it from req.Env[MCPCommandOverrideEnv],
	// which is populated ONLY for an isolated-container cell (see
	// isolation.Container.MCPCommandOverride via
	// operations.MCPCommandOverrideForPolicy); every other cell leaves it "",
	// so the host self-exec-absolute invariant is untouched.
	MCPCommandOverride string
	// DenyTools carries ManagedConfig.DenyTools through to the backend's
	// settings surface — see its doc for the deny-tools semantics.
	DenyTools []string
	// AgentName carries a backend-specific override for a materialized
	// per-agent config's own identity/name — currently only kiro's, whose
	// `--agent <name>` launch flag selects a custom agent by name (kiro's
	// settingsSurface used to always write the hardcoded default name
	// regardless of this override, so buildArgs and the materialized file
	// silently disagreed). Empty uses the backend's own default and is a
	// no-op for every other backend.
	AgentName string
}

// CellDelivery configures a launch backend's cell-based surface delivery. A
// backend that routes launch-time delivery through the typed-cell seam supplies
// one at InitLaunch; a backend that does not route launch-time delivery through
// the typed-cell seam (acp) passes nil.
type CellDelivery struct {
	// Build maps the shared per-run inputs to the backend's SurfaceSet, targeting
	// isolatedDir for any out-of-cwd (race-safe) surface — claude's append-flag /
	// --mcp-config / --settings scratch. Backends that write only well-known files
	// (codex/kiro) ignore isolatedDir. For claude the closure also
	// stashes the concrete Surfaces on the backend so buildArgs can read each
	// out-of-cwd file's Path() after delivery.
	Build func(in SurfaceInputs, isolatedDir string) SurfaceSet

	// RawContext materializes the assembled context into the content-addressed
	// cache file (agent.WriteContextFile) as a Setup pre-step and sets the
	// CTXLOOM_CONTEXT_FILE env path — matching the legacy lifecycle path for the
	// file/hook engines (codex/kiro). claude leaves it false: its
	// context rides an out-of-cwd launch flag or a well-known CLAUDE.md, never the
	// cache file.
	RawContext bool

	// ContextHook keys the SessionStart context-injection hook to the cache file's
	// hash (codex — the one engine that reads context via a hook that fires at run
	// time). kiro diverts context to AGENTS.md/steering (its context
	// surface), so its hook hash stays "". Requires RawContext (the hook reads
	// the cache file). claude leaves it false (context rides the append flag).
	ContextHook bool
}

// BuildWellKnown adapts a well-known-file backend's NewSurfaces into a
// CellDelivery.Build. Every surface writes its engine well-known path, so the
// isolated dir is ignored and the constructor runs with the default (OS)
// filesystem. It is the single source of truth for that adapter, shared by
// codex/kiro — whose Build bodies would otherwise be identical
// boilerplate. (claude is the exception: it targets the isolated dir and stashes
// its concrete Surfaces for buildArgs, so it supplies its own Build.)
func BuildWellKnown[S SurfaceSet](newSurfaces func(SurfaceInputs, afero.Fs) S) func(SurfaceInputs, string) SurfaceSet {
	return func(in SurfaceInputs, _ string) SurfaceSet { return newSurfaces(in, nil) }
}

// EmptySurfaceSet is a SurfaceSet with no surfaces. A protocol-only backend (acp)
// uses it so Setup still runs the lifecycle merge — populating the managed MCP set
// its structured chat injects over the wire (ManagedChatMCPServers) — while
// materializing no files. It lets acp share the one cell-based Setup path without
// inventing well-known files no ACP agent reads.
type EmptySurfaceSet struct{}

// SupportedApproaches reports no approaches for any kind: a protocol-only backend
// has no file surfaces, so a WithEverything selection resolves to nothing.
func (EmptySurfaceSet) SupportedApproaches(SurfaceKind) []Approach { return nil }

// DefaultApproach reports no default for any kind (nothing to select).
func (EmptySurfaceSet) DefaultApproach(SurfaceKind) (Approach, bool) { return 0, false }

// SurfaceFor resolves nothing — an empty set materializes no files, so EVERY
// (kind, approach) pair is unsupported and is refused with an error, per the
// SurfaceSet contract. Build() never reaches here (SupportedApproaches is nil
// for every kind, which Build treats as a permitted no-op), so the refusal is
// visible only to a direct caller — which is exactly the caller that would
// otherwise be handed a nil Delivery it has no error to branch on.
func (EmptySurfaceSet) SurfaceFor(kind SurfaceKind, a Approach) (Delivery, error) {
	return nil, fmt.Errorf("empty surface set: no %s surface via %s (this backend materializes no files)", kind, a)
}

// SharedRealization reports no realization for any (kind, approach) pair.
func (EmptySurfaceSet) SharedRealization(SurfaceKind, Approach) (func() (Delivered, error), bool) {
	return nil, false
}

// CellKind is the resolved isolation cell a run executes in, decided host-side
// (mapped from the isolation.Policy) and carried to the plugin over the wire so
// Setup/buildArgs know which cell they run in. It is the plugin-side mirror of
// the grpc CellKind enum. The zero value is CellKindShared, matching the wire's
// UNSPECIFIED→Shared decode. It names the same three cells as the typed cell
// values above (SharedCell / IsolatedCell).
type CellKind int

const (
	// CellKindShared is the user's live cwd — a shared directory (isolation None).
	CellKindShared CellKind = iota
	// CellKindDirectoryIsolated is a per-agent git worktree (isolation Worktree).
	CellKindDirectoryIsolated
	// CellKindProcessIsolated is a container (isolation Container, both tiers).
	CellKindProcessIsolated
)

// String renders the CellKind for diagnostics. An UNDECLARED value renders as
// "unknown(<n>)", never as the zero value: "shared" asserts a run has NO
// isolation, which is the wrong thing to report for a cell this build does not
// recognise. (The wire decode clamps unknown enum values to Shared deliberately
// — that is a decoding policy with its own doc; this is the rendering of a value
// that never went through it.)
func (k CellKind) String() string {
	switch k {
	case CellKindShared:
		return "shared"
	case CellKindDirectoryIsolated:
		return "directory-isolated"
	case CellKindProcessIsolated:
		return "process-isolated"
	default:
		return fmt.Sprintf("unknown(%d)", int(k))
	}
}

// IsolatedCell is the cell for anything that owns a PRIVATE directory — a
// per-agent WORKTREE (dir is the private checkout) or a CONTAINER (dir is the
// filesystem-namespace location the co-located in-container engine reads; the
// host-vs-guest resolution of dir from internal/lm/isolation is the seam wired
// later — plan S4; the container-mount question stays open there). A
// well-known write into a private dir cannot race another session, so an
// isolated cell accepts ANY Delivery — the Deliver signature encodes that
// safety.
//
// DirectoryIsolatedCell and ProcessIsolatedCell used to be two distinct
// (behaviourally identical) types wrapping this one, chosen between via a
// branch on req.CellKind that added nothing an isolated cell's shared Deliver
// didn't already do — collapsed to this single type. The CellKind distinction
// itself survives where it actually matters (buildArgs/env).
type IsolatedCell struct {
	dir string
}

// Deliver writes the surface to this cell's private directory via the surface's
// well-known Delivery. Accepting a plain Delivery is safe precisely because the
// directory is private.
func (c IsolatedCell) Deliver(s Delivery) (Delivered, error) {
	return s.Deliver(c.dir)
}

// NewIsolatedCell builds an isolated cell (worktree or container) that writes
// surfaces into the private dir.
func NewIsolatedCell(dir string) IsolatedCell {
	return IsolatedCell{dir: dir}
}

// surfaceOrder is the stable cross-backend delivery order — context, MCP,
// settings, commands — matching every backend's Deliveries() order, so a Build()ed
// selection's report and LIFO teardown are deterministic regardless of the order
// a caller chained the WithX() calls in.
var surfaceOrder = []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceCommands, SurfaceSkills}

// SurfaceSelection is an OPT-IN builder over a SurfaceSet: the default selects
// NOTHING, and each chainable .WithX(approach) opts one SurfaceKind in AT A NAMED
// APPROACH. Opt-in (no opt-out / "except") is deliberate — every caller states
// EXACTLY which surfaces it delivers, so a future surface kind can never silently
// ride along a broad selection. The caller ALWAYS names the approach:
// there is no silent default at the call site, and a race-capable
// choice is spelled UNSAFE_FILE so picking it IS the race acknowledgment. Build it
// with Select(set), chain the surfaces, then call Build() (or the DeliverUnder
// convenience for the at-rest callers).
type SurfaceSelection struct {
	set        SurfaceSet
	approaches map[SurfaceKind]Approach
}

// Select begins an opt-in selection over set with NOTHING selected.
func Select(set SurfaceSet) *SurfaceSelection {
	return &SurfaceSelection{set: set, approaches: map[SurfaceKind]Approach{}}
}

// WithApproach opts kind into the selection at a. It is the generic form of the
// five typed With* methods below, for a caller holding a (kind, approach) pair
// it parsed rather than one it wrote — `profile materialize --surface
// context=unsafe-file` is the reason it exists.
//
// The typed methods are NOT replaced by it and should stay preferred in code:
// WithContext(ContextWriteHook) cannot name an approach the context surface has
// no spelling for, while this can, and only Build() will catch it. That is the
// right trade for a parsed pair and the wrong one for a literal.
func (s *SurfaceSelection) WithApproach(kind SurfaceKind, a Approach) *SurfaceSelection {
	s.approaches[kind] = a
	return s
}

// WithContext opts the context surface into the selection at the named approach.
func (s *SurfaceSelection) WithContext(c ContextWrite) *SurfaceSelection {
	s.approaches[SurfaceContext] = c.approach()
	return s
}

// WithMCP opts the MCP surface into the selection at the named approach.
func (s *SurfaceSelection) WithMCP(m MCPWrite) *SurfaceSelection {
	s.approaches[SurfaceMCP] = m.approach()
	return s
}

// WithSettings opts the settings/hooks surface into the selection at the named
// approach (for engines that fold MCP or context into it, the whole folded file
// rides along).
func (s *SurfaceSelection) WithSettings(w SettingsWrite) *SurfaceSelection {
	s.approaches[SurfaceSettings] = w.approach()
	return s
}

// WithCommands opts the commands / slash-command surface into the selection at
// the named approach.
func (s *SurfaceSelection) WithCommands(w CommandsWrite) *SurfaceSelection {
	s.approaches[SurfaceCommands] = w.approach()
	return s
}

// WithSkills opts the Agent Skills surface into the selection at the named
// approach.
func (s *SurfaceSelection) WithSkills(w SkillsWrite) *SurfaceSelection {
	s.approaches[SurfaceSkills] = w.approach()
	return s
}

// WithEverything opts every PRESENT surface kind in at the backend's
// DefaultApproach — the materialize selection (a full native surface tree), and
// the selection the launch path borrows. A kind the backend folds or omits (codex's
// MCP, which rides its config/settings surface) reports no default and is
// skipped, never erroring.
func (s *SurfaceSelection) WithEverything() *SurfaceSelection {
	for _, k := range surfaceOrder {
		if a, ok := s.set.DefaultApproach(k); ok {
			s.approaches[k] = a
		}
	}
	return s
}

// Build validates every selected (kind, approach) against the backend's
// SupportedApproaches and resolves each to its concrete Delivery via SurfaceFor,
// returning a ResolvedSelection a cell/dir consumes. A selected kind the backend
// folds or omits (empty SupportedApproaches — codex's MCP) is a permitted no-op; a
// named approach the backend does not support is a loud error naming the surface,
// the requested approach, and the supported set. Selection is kept SEPARABLE from
// mechanism: Build decides only WHICH surfaces at WHICH approach; the terminal
// (DeliverUnder / DeliverShared) owns WHERE they land.
func (s *SurfaceSelection) Build() (*ResolvedSelection, error) {
	// The context Hook approach rides the settings-carried inject hook — a
	// context-only selection at Hook would write an unread cache file with no hook
	// to consume it, so Settings must be selected in the SAME Build().
	if a, ok := s.approaches[SurfaceContext]; ok && a == ApproachHook {
		if _, ok := s.approaches[SurfaceSettings]; !ok {
			return nil, fmt.Errorf("context: the hook approach rides the settings surface — select settings in the same Build()")
		}
	}

	r := &ResolvedSelection{set: s.set}
	for _, k := range surfaceOrder {
		a, ok := s.approaches[k]
		if !ok {
			continue
		}
		supported := s.set.SupportedApproaches(k)
		if len(supported) == 0 {
			// The backend has no distinct surface of this kind (codex folds MCP
			// into its config surface): selecting it delivers nothing extra.
			continue
		}
		if !containsApproach(supported, a) {
			return nil, fmt.Errorf("surface %s: approach %s not supported (supports %v)", k, a, supported)
		}
		d, err := s.set.SurfaceFor(k, a)
		if err != nil {
			return nil, err
		}
		r.surfaces = append(r.surfaces, resolvedSurface{kind: k, approach: a, delivery: d})
	}
	return r, nil
}

// containsApproach reports whether list includes a.
func containsApproach(list []Approach, a Approach) bool {
	for _, x := range list {
		if x == a {
			return true
		}
	}
	return false
}

// DeliverUnder is the convenience terminal for the at-rest callers (materialize,
// apply, remove): it Builds the selection and delivers each resolved surface into an
// IsolatedCell rooted at dir. A Build error (an unsupported approach, or
// context-Hook selected without settings) is returned as the sole entry in errs.
// See ResolvedSelection.DeliverUnder for the collect-all-failures semantics.
func (s *SurfaceSelection) DeliverUnder(dir string) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	r, err := s.Build()
	if err != nil {
		return nil, nil, []error{err}
	}
	return r.DeliverUnder(dir)
}

// resolvedSurface is one entry of a Built selection: the surface kind, the
// approach it resolved at, and the concrete Delivery to write.
type resolvedSurface struct {
	kind     SurfaceKind
	approach Approach
	delivery Delivery
}

// kindedResolvedDelivery adapts a resolved (kind, Delivery) pair into a
// KindedDelivery for an isolated cell: the kind rides the RESOLVED SELECTION
// itself, not a type-assertion on the concrete surface.
type kindedResolvedDelivery struct {
	kind SurfaceKind
	d    Delivery
}

// Deliver forwards to the wrapped Delivery.
func (k kindedResolvedDelivery) Deliver(dir string) (Delivered, error) { return k.d.Deliver(dir) }

// Kind reports the RESOLVED kind (the selection's, not a type-assertion).
func (k kindedResolvedDelivery) Kind() SurfaceKind { return k.kind }

// ResolvedSelection is a Built selection — the deliverable a cell/dir consumes. It
// carries the ordered, approach-resolved surfaces. The at-rest terminals
// (DeliverUnder / DeliverShared) live here; a cell-aware caller (the launch path)
// may instead read Deliveries() directly and drive its own cell.
type ResolvedSelection struct {
	set      SurfaceSet
	surfaces []resolvedSurface
}

// Deliveries returns the resolved surfaces as KindedDelivery, in stable order, for
// an isolated cell (worktree/container) to iterate — a well-known write into a
// private dir is safe regardless of the resolved approach. A Hook-approach
// no-op delivery (claude context riding the settings-carried hook) is included —
// its Deliver returns a nil handle, the shared "nothing written" convention.
func (r *ResolvedSelection) Deliveries() []KindedDelivery {
	out := make([]KindedDelivery, 0, len(r.surfaces))
	for _, rs := range r.surfaces {
		if rs.delivery == nil {
			continue
		}
		out = append(out, kindedResolvedDelivery{kind: rs.kind, d: rs.delivery})
	}
	return out
}

// DeliverUnder delivers each resolved surface's native (well-known) write into
// an IsolatedCell rooted at dir — the at-rest path (materialize/apply/remove),
// where a private dir makes every write race-free. It ERRORS on any surface
// resolved at ApproachSystemPrompt: that approach writes an out-of-cwd scratch file
// consumed via a launch flag, and DeliverUnder has no argv sink to hand that flag
// to — naming SystemPrompt for an at-rest delivery is a caller error, not a launch.
// It ATTEMPTS every OTHER surface and COLLECTS per-surface failures rather than
// stopping at the first — the surfaces under one root are independent, so a partial
// delivery is still useful and the caller routes failures through its own fault
// policy (fatal-by-default, or warn-and-continue under --degraded). It returns the
// handles for the surfaces that actually delivered (in order, for teardown), their
// kinds (for a delivery report), and the failures. A no-op delivery (writes
// nothing, nil handle — codex's context surface with no fragments, or claude's
// Hook no-op) is neither reported nor held, so the report reflects what was
// actually written.
func (r *ResolvedSelection) DeliverUnder(dir string) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	cell := NewIsolatedCell(dir)
	for _, rs := range r.surfaces {
		if rs.approach == ApproachSystemPrompt {
			errs = append(errs, fmt.Errorf("surface %s: system-prompt delivery has no argv sink at rest (dir %s)", rs.kind, dir))
			continue
		}
		if rs.delivery == nil {
			continue
		}
		handle, err := cell.Deliver(rs.delivery)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if handle != nil {
			delivered = append(delivered, handle)
			kinds = append(kinds, rs.kind)
		}
	}
	return delivered, kinds, errs
}

// DeliverShared delivers each resolved surface into the SHARED live cwd at dir,
// collecting per-surface failures like DeliverUnder. It is the shared-cwd
// counterpart of DeliverUnder; the launch path uses deliverOneShared directly (per
// surface) instead, so it can keep its context-failure fallback.
//
// This has no PRODUCTION call site — true, but a suggested fix ("port callers
// onto deliverOneShared directly") does not work for its actual callers:
// deliverOneShared is unexported, and every current caller of DeliverShared
// (claude/kiro/codex's surfaces_test.go, selection_test.go) lives
// in a DIFFERENT package, so it cannot reach an unexported method here.
// DeliverShared is the sanctioned, exported seam those four packages' tests
// use to exercise the shared-cwd "unsafe: warn and proceed" behavior end to
// end (real UnsafeInfo() strings, real target paths) — kept deliberately, not
// oversight.
func (r *ResolvedSelection) DeliverShared(dir string) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	for _, rs := range r.surfaces {
		d, err := r.deliverOneShared(rs, dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if d != nil {
			delivered = append(delivered, d)
			kinds = append(kinds, rs.kind)
		}
	}
	return delivered, kinds, errs
}

// isolatedDelivery is optionally implemented by a concrete surface Delivery that
// can write its content OUT of the shared cwd (claude's context/MCP/settings
// scratch files, consumed via a launch flag). It is the structural proof that a
// backend's kind-keyed SharedRealization belongs to THIS resolved surface: the
// realization closure is a method value bound to the very instance that carries
// the method, so a resolved delivery lacking it is a different object and must
// not be converted.
type isolatedDelivery interface {
	DeliverIsolated() (Delivered, error)
}

// unsafeNamed is optionally implemented by a concrete surface Delivery to
// self-describe for the DeliverShared warning (e.g. "claude/commands"). A surface
// without it (the shared codex/kiro ManagedCommandsDelivery) falls back
// to its cross-backend SurfaceKind label.
type unsafeNamed interface {
	UnsafeInfo() string
}

// deliverOneShared delivers ONE resolved surface into the SHARED live cwd at dir.
// When the backend offers a SharedRealization for this (kind, approach) pair
// (claude's out-of-cwd scratch conversion, offered only at ApproachSystemPrompt
// for context), it runs that closure — genuinely race-safe, no warning.
// Otherwise the well-known write lands directly in the shared cwd: loudly warned
// first, since the selected UNSAFE_FILE approach is the caller's acknowledgment
// that ctxloom does not lock projects — this is also where an explicit
// context=unsafe-file preference on a shared cell lands (U100-F05's fork:
// honored, not silently converted to the scratch, and not refused). A nil
// delivery (claude's Hook no-op) is itself a no-op.
func (r *ResolvedSelection) deliverOneShared(rs resolvedSurface, dir string) (Delivered, error) {
	if rs.delivery == nil {
		return nil, nil
	}
	// SharedRealization is keyed on the (kind, approach) PAIR: a kind can
	// resolve to different deliveries per approach, and — since U100-F05 —
	// different approaches of the SAME dual-capable delivery can realize
	// differently. claude's context resolves to the dual-capable contextSurface
	// at both UnsafeFile and SystemPrompt, but only SystemPrompt has a
	// realization; UnsafeFile is the caller's explicit request to write the
	// native file, honored below with a warning (the fork this method already
	// implements). Hook resolves to a documented no-op. Requiring the RESOLVED
	// delivery to carry the isolated path is what keeps the pair-keyed lookup
	// from converting a surface the caller asked not to write: running the
	// scratch writer for a hook-carried context would emit
	// --append-system-prompt-file alongside the inject hook and DOUBLE the
	// context.
	if _, isolatable := rs.delivery.(isolatedDelivery); isolatable {
		if realize, ok := r.set.SharedRealization(rs.kind, rs.approach); ok {
			return realize()
		}
	}
	info := rs.kind.String()
	if n, ok := rs.delivery.(unsafeNamed); ok {
		info = n.UnsafeInfo()
	}
	Warn("unsafe: %s into shared cwd %s — no isolated mechanism; races concurrent agents", info, dir)
	return rs.delivery.Deliver(dir)
}
