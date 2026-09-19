package agent

import (
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// PermissionMode is engine.PermissionMode: the vocabulary is declared once,
// in core/engine, and this package carries its established names forward for
// its own callers. ResolveDefault stays here because it reports through
// strictness, which the engine vocabulary must not know.
type PermissionMode = engine.PermissionMode

const (
	PermissionNotRequested = engine.PermissionNotRequested
	PermissionDefault      = engine.PermissionDefault
	PermissionAcceptEdits  = engine.PermissionAcceptEdits
	PermissionPlan         = engine.PermissionPlan
	PermissionBypass       = engine.PermissionBypass
	PermissionFloor        = engine.PermissionFloor
)

// ParsePermissionMode is engine.ParsePermissionMode.
func ParsePermissionMode(s string) (PermissionMode, bool) { return engine.ParsePermissionMode(s) }

// PermissionModeNames is engine.PermissionModeNames.
func PermissionModeNames() []string { return engine.PermissionModeNames() }

// WireMode is engine.WireMode.
func WireMode(s string) PermissionMode { return engine.WireMode(s) }

// ResolveDefault picks the posture from layered source spellings — the first
// DECLARED of the ordered sources wins (e.g. flag > agent > label) — falling
// back to hostDefault when nothing is declared: the engine's declared
// default posture for a host run (engine.PermissionFacts.HostDefault), which
// the caller reads off the engine's Definition so this stays engine-free.
// It is the run-context-independent base shared by the run resolver and
// `agent show`; callers layer CollapsePlanIfUnenforced and the headless
// floor on top.
//
// UNSET AND UNPARSEABLE ARE DIFFERENT INPUTS, and conflating them was a silent
// privilege escalation. An empty source declares nothing and is skipped, so a
// lower source (and ultimately the built-in default) answers for it. A NON-EMPTY
// source that does not parse is a declaration that MISSED: the user asked for
// something, so continuing down the chain hands them a posture nobody chose —
// on an engine whose host default is bypass, the bottom of that chain is
// bypass, so `permissions: plann` (an obvious `plan`) resolved to full
// --dangerously-skip-permissions, and the escalation ladder derived from it
// flipped from auto-decline to auto-accept. A missed declaration therefore
// STOPS the chain, reports a fatal ClassConfig finding, and floors to
// PermissionFloor. honoured is false in exactly that case, so a caller must
// not apply any widening step (the ONESHOT floor, a backend collapse) to the
// returned mode: nothing may lift a posture nobody successfully declared.
// Degraded mode narrows here too — it never widens.
func ResolveDefault(rep report.Reporter, sources []string, hostDefault PermissionMode) (mode PermissionMode, honoured bool) {
	fallback := hostDefault
	if fallback == PermissionNotRequested {
		fallback = PermissionDefault
	}
	for _, s := range sources {
		if strings.TrimSpace(s) == "" {
			continue
		}
		m, ok := ParsePermissionMode(s)
		if !ok {
			rep.FailOncef(report.KindConfig,
				fmt.Sprintf("set permissions: to one of %s (fix the typo in .ctxloom/config.yaml, the --permissions flag, or the --config-set override)", strings.Join(PermissionModeNames(), "|")),
				"unknown permissions value %q (known: %s); an unrecognised posture is NOT treated as unset — it would otherwise resolve to %q, so this run is floored to %q (read-only)",
				s, strings.Join(PermissionModeNames(), "|"), fallback, PermissionFloor)
			return PermissionFloor, false
		}
		return m, true
	}
	return fallback, true
}
