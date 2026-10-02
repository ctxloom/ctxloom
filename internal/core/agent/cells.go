package agent

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// This file holds the cross-backend surface vocabulary (SurfaceKind and its
// parser), the per-run inputs every approach is built from (SurfaceInputs),
// and the refusal an approach gives when the root it writes beneath was never
// resolved for the run.

// ErrUnrootedSessionHome is returned by an approach that writes beneath the
// SESSION HOME when that root was never resolved for the run. A "" root
// joined into a well-known path yields a bare relative path that lands
// wherever the process happens to be; and the tempting fallback — the engine's REAL home, ~/.claude and the
// like — is the user's own, shared across every session, and writing it
// because a private one was not advised is the shared/dangerous default the
// seam refuses to take on anyone's behalf. The remedy is in the message,
// because the refusal is the whole interface for the failure.
var ErrUnrootedSessionHome = errors.New("delivery: the session home was never resolved — this approach writes beneath the session's private home, which a run whose agent binding selects engine_home: host does not have; drop that selection, or select a project-file approach for this surface")

// SessionHomeRooted is the entry check for an approach that lands beneath the
// session home. It is exported
// because the approaches that need it live in the engine packages, and the
// seam wants them to refuse the same way rather than each inventing its
// own check.
func SessionHomeRooted(start present.Start) error {
	if start.Paths().SessionHome.Host == "" {
		return ErrUnrootedSessionHome
	}
	return nil
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
// humans (`agent create --surface context=unsafe-file`), not only
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

// SurfaceInputs is the shared, per-run superset of everything a backend's
// surfaces write: the assembled context (as a string, and as the raw fragments
// for an approach that assembles its own), the merged MCP config
// + profile/companion bundle servers, the merged hook set + statusline policy, and
// the command exports. A caller fills it once and hands it to every
// approach's Construct, which picks the fields IT
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
}
