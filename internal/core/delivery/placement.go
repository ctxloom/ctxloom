package delivery

import (
	"slices"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// PlanFor is THE planner for an at-rest delivery and a launch alike: the
// placement core's planning half. Every kind the Definition does not carry
// is an accepted loss (the one rule ProjectPlan and the launch preference
// both stated); roots selects a root per kind (nil: each approach's first
// offered root that paths has — over a project-only cell, the project
// root); Route decides; then the entries and losses outside kinds are
// dropped (nil kinds: all stay).
//
// endpoint says whether this placement serves the session's MCP endpoint (a
// live session). ctxloom's own endpoint is session-scoped and never written
// at rest, so where endpoint is false an MCP route that exists only for it
// — no declared server other than the session-endpoint declaration — is
// dropped: an at-rest MCP file carries the profiles' servers and nothing
// else. InputsFor renders nothing for the declaration at rest either; this
// keeps the plan from naming a kind with nothing to deliver.
func PlanFor(root engine.Base, items engine.Items, paths present.Paths, roots map[present.Kind]present.RootKind, kinds []present.Kind, endpoint bool) (Plan, error) {
	pref := Preference{Root: map[present.Kind]present.RootKind{}, AcceptLoss: map[present.Kind]bool{}}
	for k, r := range roots {
		pref.Root[k] = r
	}
	for _, k := range AllKinds() {
		if !root.Carries(k) {
			pref.AcceptLoss[k] = true
		}
	}
	plan, err := Route(items, root, pref, paths)
	if err != nil {
		return Plan{}, err
	}
	keep := func(k present.Kind) bool {
		if k == present.MCP && !endpoint && !declaresServers(items) {
			return false
		}
		return kinds == nil || slices.Contains(kinds, k)
	}
	plan.Static = slices.DeleteFunc(plan.Static, func(it StaticItem) bool { return !keep(it.Kind) })
	plan.Losses = slices.DeleteFunc(plan.Losses, func(l Loss) bool { return !keep(l.Kind) })
	return plan, nil
}

// declaresServers reports whether items carry an MCP server other than the
// session-endpoint declaration.
func declaresServers(items engine.Items) bool {
	return slices.ContainsFunc(items.MCP, func(s wire.MCPServer) bool { return !s.IsSessionEndpoint() })
}

// TargetFor is THE target constructor: start's roots, recorded in records,
// under family — one writer for every kind when kinds is nil (a session),
// split per kind (Writer.Of) when it is set (an at-rest delivery).
func TargetFor(start present.Start, records Ownership, family Writer, kinds []present.Kind) Target {
	return Target{Root: start, Ownership: records, Writer: family, Kinds: kinds}
}
