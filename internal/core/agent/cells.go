package agent

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// This file is the type-level FOUNDATION of ctxloom's unified surface-delivery
// seam: the Delivery interface every surface implements, the typed isolation
// cells that land a well-known write in a private dir, and the name-keyed
// SurfaceSelection builder that resolves a caller's named per-surface approach
// against the engine's Declaration and constructs it. Race-safety is handled
// by the CELL and by the APPROACH, not a parallel type hierarchy: an isolated
// cell's private dir makes any well-known write race-free by construction, and
// on a SHARED cwd an approach that presents outside the project root is already
// safe, while a well-known write gets a loud warning — the caller's
// ApproachUnsafeFile choice IS that warning's acknowledgment.
//
// What a shared cwd never does is SUBSTITUTE. A caller that named an approach
// gets that approach or an error, never a different one reported as success
// (see deliverOneShared, and agent.OutOfCwd for the one residual conversion).
//
// (Delivered — the handle owning a delivery's cleanup — is defined in
// delivery.go and reused here.)

// Delivery writes one surface of a loadout to its well-known path — the
// engine's native location (CLAUDE.md, .mcp.json, AGENTS.md, .claude/…) —
// beneath the ADVISED roots it is handed. Every surface implements it. It
// returns a Delivered handle owning the cleanup that reverses the write.
//
// Deliver receives what the pre-advice produced, never a bare directory
// string: the same present.Start every Presenter composes from, with every
// root already resolved for THIS run (host, worktree or container). The
// presenter DECIDES where bytes go; Deliver ACTS. A surface therefore cannot
// compute a location of its own from a string it was handed, and a run whose
// roots differ from the last one (a worktree, a relocated home) reaches the
// writer through the same value that reached its presenter.
type Delivery interface {
	// Deliver materializes the surface at its well-known location beneath the
	// advised roots and returns a handle owning its cleanup.
	Deliver(start present.Start) (Delivered, error)
}

// ErrUnrootedDelivery is returned when a delivery is attempted against a Start
// whose project root was never resolved. A "" root joined into a well-known
// path yields a BARE RELATIVE path that looks well-formed and lands wherever
// the process happens to be — the silent failure the open-sets ruling names as
// the dangerous one — so the seam refuses it loudly at the point the Start
// enters, before any surface can act on it.
var ErrUnrootedDelivery = errors.New("delivery: the project root was never resolved")

// rooted is the seam's entry check: every path a Start enters a Delivery
// through (the isolated cell, the shared-cwd delivery) passes it first, so an
// unresolved project root is refused once, in one place.
func rooted(start present.Start) error {
	if start.Paths().ProjectRoot.Host == "" {
		return ErrUnrootedDelivery
	}
	return nil
}

// ErrUnrootedEngineHome is returned by an approach that writes beneath the
// ENGINE HOME when that root was never resolved for the run. The same
// bare-relative-path hazard ErrUnrootedDelivery names applies, and one
// more: the tempting fallback — the engine's REAL home, ~/.claude and the
// like — is the user's own, shared across every session, and writing it
// because a private one was not advised is the shared/dangerous default the
// seam refuses to take on anyone's behalf. The remedy is in the message,
// because the refusal is the whole interface for the failure.
var ErrUnrootedEngineHome = errors.New("delivery: the engine home was never resolved — this approach writes beneath the engine's private config home, which only a run whose agent binding declares engine_home: session advises; declare it on the binding, or select a project-file approach for this surface")

// ErrNoArgvSinkAtRest is returned for a LaunchOnly approach asked to deliver AT
// REST. Such an approach announces its payload on a launch flag, and at rest
// there is no argv to carry one — so the delivery would write a file nothing
// ever points the engine at. Refusing is the declared behaviour, not a failure
// of the run: callers that legitimately deliver the whole surface set at rest
// match on this sentinel rather than on the message text.
var ErrNoArgvSinkAtRest = errors.New("delivery has no argv sink at rest")

// ErrAbsentSharedSurface is returned when a run on LaunchFormPresent names a
// surface the session never delivered. "Use the existing surface" has exactly
// one honest failure: there is no existing surface. Writing one instead would
// clobber the shared cwd this form exists to protect, and re-injecting the
// content down a second route is the silent degrade the form exists to remove —
// so this refuses, naming what is missing.
var ErrAbsentSharedSurface = errors.New("launch: this run presents the session's existing surfaces and writes none of its own, but one of them is not there — the session it rides was never set up, or its scratch was cleared; run the session's own setup first, or give this run an isolated cell so it delivers its own")

// RequireDelivered is the entry check for LaunchFormPresent: it is exported
// because the approaches that implement PresentExisting live in the engine
// packages, and the seam wants them to refuse the same way rather than each
// inventing its own stat. kind and path name what is missing, so the refusal
// says which surface and where it was looked for.
func RequireDelivered(fs afero.Fs, kind SurfaceKind, path string) error {
	ok, err := afero.Exists(GetFS(fs), path)
	if err != nil {
		return fmt.Errorf("%w: checking the %s surface at %s: %w", ErrAbsentSharedSurface, kind, path, err)
	}
	if !ok {
		return fmt.Errorf("%w: the %s surface is not at %s", ErrAbsentSharedSurface, kind, path)
	}
	return nil
}

// EngineHomeRooted is the entry check for an approach that lands beneath the
// engine home: the counterpart of rooted for that root. It is exported
// because the approaches that need it live in the engine packages, and the
// seam wants them to refuse the same way rather than each inventing its
// own check.
func EngineHomeRooted(start present.Start) error {
	if start.Paths().EngineHome.Host == "" {
		return ErrUnrootedEngineHome
	}
	return nil
}

// PresentsUnderProjectRoot reports whether a's bytes land beneath the
// PROJECT ROOT — as a well-known file an engine started in that directory
// reads — by asking its presenter, against sentinel roots that cannot be
// confused with one another. It is the ONE answer to "is this delivery a
// project file?", consulted by the shared-cwd warning here and by the
// at-rest install route, so the two cannot disagree.
//
// It reads the PRESENTATION rather than enumerating marker interfaces
// (Rider, LaunchOnly) because a marker list is a closed set that a new
// approach silently falls outside of: a record-backed write beneath the
// engine home is neither a rider nor launch-only, and by markers alone it
// would be classed a project file. The presenter already states where the
// bytes go — "the presenter decides where bytes go; Deliver acts" — so the
// predicate reads that decision. A Rider presents nothing (no HostPath); a
// launch-only scratch form presents under Scratch; both are correctly "not
// a project file" without being named here.
func PresentsUnderProjectRoot(a Approach) bool {
	const project = "/ctxloom-probe/project"
	probe := present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: project},
		EngineHome:  present.Root{Host: "/ctxloom-probe/engine-home"},
		CtxloomHome: present.Root{Host: "/ctxloom-probe/ctxloom-home"},
		Scratch:     present.Root{Host: "/ctxloom-probe/scratch"},
	}))
	host := filepath.ToSlash(a.Present(probe).HostPath)
	return host == project || strings.HasPrefix(host, project+"/")
}

// SafeInSharedCwd reports whether delivering a through a SHARED-cwd launch
// races a concurrent session using the same project. It is the predicate a
// shared launch derives its preference from, and it reads the APPROACH rather
// than a name, so an engine's naming decides nothing.
//
// A Rider writes no bytes of its own; an approach whose presentation lands
// outside the project root (the framed system prompt, the default .mcp.json)
// is not a write into the shared cwd at all. Both are safe. A well-known
// project file is not, and choosing it anyway is the caller's acknowledged
// race — deliverOneShared warns and proceeds.
//
// The OutOfCwd disjunct is RESIDUE, and is the reason this is not simply
// !Rider && !PresentsUnderProjectRoot. Settings is the last approach carrying
// a second form: it presents as a project file but converts to a private one
// when a shared launch delivers it. Splitting it the way MCP and the system
// prompt were split is unsafe while an isolated cell's scratch IS its checkout
// — a private --settings file would land at the well-known path AND be
// announced on the flag, registering claude's hooks twice. When settings gains
// its one form this disjunct goes, and the predicate reduces to the two terms
// above.
func SafeInSharedCwd(a Approach) bool {
	if _, rider := a.(Rider); rider {
		return true
	}
	if _, converts := a.(OutOfCwd); converts {
		return true
	}
	return !PresentsUnderProjectRoot(a)
}

// SurfaceKind is present.Kind under this package's established name: the
// cross-backend category a delivery surface belongs to. The vocabulary is
// declared once, in core/present; the parser below stays here because it
// walks this package's surfaceOrder.
type SurfaceKind = present.Kind

const (
	SurfaceContext  = present.Context
	SurfaceMCP      = present.MCP
	SurfaceSettings = present.Settings
	SurfaceHooks    = present.Hooks
	SurfaceCommands = present.Commands
	SurfaceSkills   = present.Skills
)

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

// SurfaceInputs is the shared, per-run superset of everything a backend's
// surfaces write: the assembled context (as a string for the ContextWriter-core
// engines, and the raw fragments for codex's file writer), the merged MCP config
// + profile/builtin bundle servers, the merged hook set + statusline policy, and
// the command exports. Setup fills it once (from req + the merged lifecycle state)
// and hands it to every selected approach's Construct, which picks the fields
// IT needs. It is the cross-backend contract that lets the generic Setup build
// any engine's approaches without importing the concrete engine.
type SurfaceInputs struct {
	// Reporter is where the approaches built from these inputs report; the
	// engine forwards it into its writers. Nil discards.
	Reporter         report.Sink
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
// per-agent WORKTREE (the project root is the private checkout) or a CONTAINER
// (the project root is the filesystem-namespace location the co-located
// in-container engine reads). It holds the run's ADVISED roots, resolved once
// before any surface runs, and hands them to every Delivery unchanged. A
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
	start present.Start
}

// Deliver writes the surface beneath this cell's advised roots via the
// surface's well-known Delivery. Accepting a plain Delivery is safe precisely
// because the project root is private. An unresolved project root is refused
// (ErrUnrootedDelivery) before the surface runs.
func (c IsolatedCell) Deliver(s Delivery) (Delivered, error) {
	if err := rooted(c.start); err != nil {
		return nil, err
	}
	return s.Deliver(c.start)
}

// NewIsolatedCell builds an isolated cell (worktree or container) over the
// run's advised roots; every surface delivered through it writes beneath
// start's project root.
func NewIsolatedCell(start present.Start) IsolatedCell {
	return IsolatedCell{start: start}
}

// surfaceOrder is the stable cross-backend delivery order — every Kind, in
// Kind order — matching every backend's Deliveries() order, so a Build()ed
// selection's report and LIFO teardown are deterministic regardless of the order
// a caller chained the WithX() calls in.
var surfaceOrder = []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceHooks, SurfaceCommands, SurfaceSkills}

// SurfaceSelection is an OPT-IN builder over an engine's Declaration: the
// default selects NOTHING, and each With(kind, name) opts one SurfaceKind in
// AT A NAMED APPROACH. Opt-in (no opt-out / "except") is deliberate — every
// caller states EXACTLY which surfaces it delivers, so a future surface kind
// can never silently ride along a broad selection. The caller ALWAYS names
// the approach: there is no silent default at the call site, and a
// race-capable choice is spelled ApproachUnsafeFile so picking it IS the race
// acknowledgment. Build it with Select(decl), chain the surfaces, then call
// Build (or the DeliverUnder convenience for the at-rest callers).
//
// Selection is SEPARABLE from construction and from mechanism: With records
// names only; Build constructs exactly the selected approaches from the run's
// content; the terminal (DeliverUnder / DeliverShared / a launch cell) owns
// WHERE they land.
type SurfaceSelection struct {
	decl  Declaration
	names map[SurfaceKind]string
}

// Select begins an opt-in selection over decl with NOTHING selected.
func Select(decl Declaration) *SurfaceSelection {
	return &SurfaceSelection{decl: decl, names: map[SurfaceKind]string{}}
}

// With opts kind into the selection at the named approach. The name is
// validated at Build against the Declaration — a name the engine cannot
// construct is a loud error there, never a fallback.
func (s *SurfaceSelection) With(kind SurfaceKind, name string) *SurfaceSelection {
	s.names[kind] = name
	return s
}

// WithEverything opts every DECLARED surface kind in at the engine's default
// — the materialize selection (a full native surface tree), and the selection
// the launch path borrows. A kind the engine folds or omits is absent from
// the Declaration and is skipped, never erroring.
func (s *SurfaceSelection) WithEverything() *SurfaceSelection {
	for _, k := range surfaceOrder {
		if def, ok := s.decl.Default(k); ok {
			s.names[k] = def
		}
	}
	return s
}

// Build validates every selected (kind, name) against the Declaration and
// constructs each from the run's content, returning a ResolvedSelection a
// cell/dir consumes. A selected kind the engine does not declare is a
// permitted no-op; a name the engine does not declare for a kind it does have
// is a loud error naming the surface, the requested name and the declared set.
// A Rider (hook-carried context) is refused unless the kind it rides is
// selected in the SAME Build: delivered alone it would write nothing and
// report success.
func (s *SurfaceSelection) Build(in SurfaceInputs, fs afero.Fs) (*ResolvedSelection, error) {
	r := &ResolvedSelection{rep: report.To(in.Reporter), decl: s.decl}
	for _, k := range surfaceOrder {
		name, ok := s.names[k]
		if !ok {
			continue
		}
		p, declared := s.decl[k]
		if !declared {
			// The engine has no distinct surface of this kind (it folds MCP
			// into its settings file): selecting it delivers nothing extra.
			continue
		}
		a, ok := p.Construct(name, in, fs)
		if !ok {
			return nil, fmt.Errorf("%s: surface %s: approach %q not supported (supports %s)", p.Engine(), k, name, strings.Join(p.Names(), ", "))
		}
		if rider, ok := a.(Rider); ok {
			if _, selected := s.names[rider.Rides()]; !selected {
				return nil, fmt.Errorf("%s: the %s approach rides the %s surface — select %s in the same Build()", k, name, rider.Rides(), rider.Rides())
			}
		}
		r.surfaces = append(r.surfaces, resolvedSurface{kind: k, name: name, approach: a})
	}
	return r, nil
}

// DeliverUnder is the convenience terminal for the at-rest callers
// (materialize, apply, remove): it Builds the selection from the run's
// content and delivers each resolved surface into an IsolatedCell over the
// advised roots. A Build error (an undeclared approach, or a rider without
// the surface it rides) is returned as the sole entry in errs. See
// ResolvedSelection.DeliverUnder for the collect-all-failures semantics.
func (s *SurfaceSelection) DeliverUnder(in SurfaceInputs, fs afero.Fs, start present.Start) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	r, err := s.Build(in, fs)
	if err != nil {
		return nil, nil, []error{err}
	}
	return r.DeliverUnder(start)
}

// resolvedSurface is one entry of a Built selection: the surface kind, the
// approach name it resolved at, and the constructed Approach to write.
type resolvedSurface struct {
	kind     SurfaceKind
	name     string
	approach Approach
}

// ResolvedApproach is one Built surface as a caller sees it: a launch backend
// reads these after delivery to learn what its engine's approaches recorded
// (claude's out-of-cwd file paths for its launch flags).
type ResolvedApproach struct {
	Kind     SurfaceKind
	Name     string
	Approach Approach
}

// kindedResolvedDelivery adapts a resolved (kind, Approach) pair into a
// KindedDelivery for an isolated cell: the kind rides the RESOLVED SELECTION
// itself, not a type-assertion on the concrete surface.
type kindedResolvedDelivery struct {
	kind SurfaceKind
	d    Delivery
}

// Deliver forwards to the wrapped Delivery.
func (k kindedResolvedDelivery) Deliver(start present.Start) (Delivered, error) {
	return k.d.Deliver(start)
}

// Kind reports the RESOLVED kind (the selection's, not a type-assertion).
func (k kindedResolvedDelivery) Kind() SurfaceKind { return k.kind }

// ResolvedSelection is a Built selection — the deliverable a cell/dir
// consumes. It carries the ordered, constructed surfaces. The at-rest
// terminals (DeliverUnder / DeliverShared) live here; a cell-aware caller
// (the launch path) may instead read Deliveries() directly and drive its own
// cell.
type ResolvedSelection struct {
	rep      report.Reporter
	decl     Declaration
	surfaces []resolvedSurface
}

// Approaches lists the resolved surfaces in delivery order.
func (r *ResolvedSelection) Approaches() []ResolvedApproach {
	out := make([]ResolvedApproach, 0, len(r.surfaces))
	for _, rs := range r.surfaces {
		out = append(out, ResolvedApproach{Kind: rs.kind, Name: rs.name, Approach: rs.approach})
	}
	return out
}

// Deliveries returns the resolved surfaces as KindedDelivery, in stable order,
// for an isolated cell (worktree/container) to iterate — a well-known write
// into a private dir is safe regardless of the resolved approach. A Rider's
// no-op delivery (hook-carried context) is included — its Deliver returns a
// nil handle, the shared "nothing written" convention.
func (r *ResolvedSelection) Deliveries() []KindedDelivery {
	out := make([]KindedDelivery, 0, len(r.surfaces))
	for _, rs := range r.surfaces {
		out = append(out, kindedResolvedDelivery{kind: rs.kind, d: rs.approach})
	}
	return out
}

// DeliverUnder delivers each resolved surface's native (well-known) write into
// an IsolatedCell over the advised roots — the at-rest path
// (materialize/apply/remove), where a private dir makes every write race-free.
// It ERRORS on any surface resolved at a LaunchOnly approach: that approach's
// bytes are announced by a launch flag, and DeliverUnder has no argv sink to
// hand that flag to — naming it for an at-rest delivery is a caller error, not
// a launch. It ATTEMPTS every OTHER surface and COLLECTS per-surface failures
// rather than stopping at the first — the surfaces under one root are
// independent, so a partial delivery is still useful and the caller routes
// failures through its own fault policy (fatal-by-default, or
// warn-and-continue under --degraded). It returns the handles for the
// surfaces that actually delivered (in order, for teardown), their kinds (for
// a delivery report), and the failures. A no-op delivery (writes nothing, nil
// handle — a context surface with no fragments, or a Rider) is neither
// reported nor held, so the report reflects what was actually written.
func (r *ResolvedSelection) DeliverUnder(start present.Start) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	cell := NewIsolatedCell(start)
	for _, rs := range r.surfaces {
		if _, launchOnly := rs.approach.(LaunchOnly); launchOnly {
			errs = append(errs, fmt.Errorf("surface %s: %s %w (dir %s)", rs.kind, rs.name, ErrNoArgvSinkAtRest, start.Paths().ProjectRoot.Host))
			continue
		}
		handle, err := cell.Deliver(rs.approach)
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

// DeliverShared delivers each resolved surface into the SHARED live cwd — the
// advised project root — collecting per-surface failures like DeliverUnder. It
// is the shared-cwd counterpart of DeliverUnder; the launch path uses
// deliverOneShared directly (per surface) instead, so it can keep its
// context-failure fallback.
//
// This has no PRODUCTION call site — true, but its callers (the engine
// packages' surface tests) live in a DIFFERENT package and cannot reach the
// unexported deliverOneShared. It is the sanctioned, exported seam those tests
// use to exercise the shared-cwd "unsafe: warn and proceed" behaviour end to
// end (real UnsafeInfo() strings, real target paths) — kept deliberately.
func (r *ResolvedSelection) DeliverShared(start present.Start) (delivered []Delivered, kinds []SurfaceKind, errs []error) {
	for _, rs := range r.surfaces {
		d, err := r.deliverOneShared(rs, start)
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

// unsafeNamed is optionally implemented by a concrete Approach to
// self-describe for the DeliverShared warning (e.g. "claude/commands"). One
// without it falls back to its cross-backend SurfaceKind label.
type unsafeNamed interface {
	UnsafeInfo() string
}

// deliverOneShared delivers ONE resolved surface into the SHARED live cwd —
// the advised project root. When the approach has an OutOfCwd form it runs
// THAT against the same advised roots — it writes beneath Scratch —
// genuinely race-safe, no warning. That branch is RESIDUE: it applies to
// settings alone, the last approach with a second form (see agent.OutOfCwd).
// It is a CONVERSION, and a conversion is exactly what an explicitly named
// approach must not get, which is why context and MCP no longer reach it —
// each declares one approach per behaviour, and an approach whose bytes land
// outside the project root falls through to the plain Deliver below.
// Otherwise the well-known write lands
// directly in the shared cwd: loudly warned first, since the selected
// ApproachUnsafeFile is the caller's acknowledgment that ctxloom does not lock
// projects — this is also where an explicit context=unsafe-file preference on
// a shared cell lands (honoured, not silently converted to the scratch, and
// not refused). A Rider's no-op delivery is itself a no-op: it writes nothing
// and is not warned.
//
// Whether an approach converts is the APPROACH's own property, carried by the
// very value that was constructed for it: claude's context declares the
// native file WITHOUT an OutOfCwd form and its system prompt WITH one, so the
// pair-keyed lookup that used to guard "do not convert a surface the caller
// asked not to write" is no longer needed — the value asked for is the value
// that decides.
func (r *ResolvedSelection) deliverOneShared(rs resolvedSurface, start present.Start) (Delivered, error) {
	if _, rider := rs.approach.(Rider); rider {
		return rs.approach.Deliver(start)
	}
	if err := rooted(start); err != nil {
		return nil, err
	}
	if o, ok := rs.approach.(OutOfCwd); ok {
		return o.DeliverIsolated(start)
	}
	// An approach that lands OUTSIDE the project root — beneath the engine's
	// per-session home, say — is not a write into the shared cwd at all, so
	// there is no race to warn about; only a well-known project file is.
	if !PresentsUnderProjectRoot(rs.approach) {
		return rs.approach.Deliver(start)
	}
	info := rs.kind.String()
	if n, ok := rs.approach.(unsafeNamed); ok {
		info = n.UnsafeInfo()
	}
	Warn("unsafe: %s into shared cwd %s — no isolated mechanism; races concurrent agents", info, start.Paths().ProjectRoot.Host)
	return rs.approach.Deliver(start)
}
