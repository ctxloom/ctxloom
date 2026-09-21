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

// surfaceOrder is the stable cross-engine surface order — every Kind, in
// Kind order — so the names ParseSurfaceKind reads and SurfaceKindNames
// renders are one enumeration.
var surfaceOrder = []SurfaceKind{SurfaceContext, SurfaceMCP, SurfaceSettings, SurfaceHooks, SurfaceCommands, SurfaceSkills}

// SurfaceKindNames lists every kind's label, in kind order. It reads
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
// + profile/companion bundle servers, the merged hook set + statusline policy, and
// the command exports. A caller fills it once and hands it to every
// approach's Construct (Declaration.Construct), which picks the fields IT
// needs. It is the cross-backend contract that lets a caller build any
// engine's approaches without importing the concrete engine.
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

// CellKind is the resolved isolation cell a run executes in, decided from
// the launch's cell (cli.cellKindOf) and carried on ExecuteRequest so an
// engine's env knows which cell it runs in. The zero value is CellKindShared.
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
