package agent

import "strings"

// SurfaceLoss names one piece of an assembled loadout that the chosen engine has
// NO structural place for — the inverse of a delivery report's "wrote" line.
//
// A delivery report can only ever list what it wrote, and every line of it is
// true; the loss is what is missing from that list, and a reader has no way to
// tell "this engine has no hooks" apart from "this profile declared no hooks".
// Both look like silence. Materializing a team profile onto an engine with no
// hook mechanism dropped the team's guardrail and said nothing (whiny-exclusive),
// which is this codebase's characteristic failure wearing a report.
//
// It is the whole-mechanism analogue of HookRoute.Unsupported, which says the
// same thing one level down (this engine has no native event for THIS kind) and
// for the same reason: a drop nobody declared cannot be told apart from an
// oversight.
type SurfaceLoss struct {
	// Surface is the cross-engine name of what could not be carried, in the
	// user's own vocabulary — the word they wrote in their config ("hooks"),
	// not an internal kind.
	Surface string `json:"surface"`
	// Detail names WHAT was lost, concretely enough to go and look at: the
	// per-kind counts of the dropped hooks ("1 session_start, 2 pre_tool").
	// Empty when the loss has no further breakdown.
	Detail string `json:"detail,omitempty"`
	// Reason is the one-clause explanation of why this engine cannot carry it
	// ("opencode has no hook mechanism"). It is a property of the ENGINE, not
	// of this run, so it reads the same every time and can be believed.
	Reason string `json:"reason"`
}

// String renders a loss as one report line: "hooks (1 session_start) — opencode
// has no hook mechanism".
func (l SurfaceLoss) String() string {
	var b strings.Builder
	b.WriteString(l.Surface)
	if l.Detail != "" {
		b.WriteString(" (")
		b.WriteString(l.Detail)
		b.WriteString(")")
	}
	if l.Reason != "" {
		b.WriteString(" — ")
		b.WriteString(l.Reason)
	}
	return b.String()
}
