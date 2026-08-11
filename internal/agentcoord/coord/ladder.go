package coord

import (
	"fmt"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agents"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// C2 — THE ESCALATION LADDER (policy surface). A ladder is an ORDERED list
// of rungs; each answers a subset of ApprovalRequest kinds one of four ways.
// serveApproval (runchannel.go) walks the rungs matching a request's kind IN
// ORDER: auto_accept/auto_decline resolve immediately; relay_to_role/
// surface_to_human park the request as mail to the target role and wait up
// to the rung's timeout — a timeout FALLS THROUGH to the next matching
// rung. Running off the end of the ladder bottoms at CANCEL — nobody
// decided, so nothing is reported as a decision (never a hang, never a
// silent human-shaped gap, and never a refusal attributed to an operator who
// was never asked). ACCEPT_FOR_SESSION caches (run, kind) at
// the coordinator (coordinator.go) and short-circuits the whole walk on a
// later like-kind request.

// LadderAction is one rung's disposition.
type LadderAction string

const (
	ActionAutoAccept     LadderAction = "auto_accept"
	ActionAutoDecline    LadderAction = "auto_decline"
	ActionRelayToRole    LadderAction = "relay_to_role"
	ActionSurfaceToHuman LadderAction = "surface_to_human"
)

// LadderRung is one ordered, VALIDATED entry of an escalation ladder — the
// parsed form of agents.EscalationRung (kind names resolved to the proto
// enum, timeout parsed, action/role validated) that serveApproval consumes.
type LadderRung struct {
	// Kinds are the ApprovalKinds this rung answers; empty matches every
	// kind (a catch-all rung).
	Kinds  []agentcoordpb.ApprovalRequest_ApprovalKind
	Action LadderAction
	// Role is the relay_to_role/surface_to_human mail target — always
	// ParentAddress in this window (flat-hub topology; validated at build).
	Role    string
	Timeout time.Duration
}

// Ladder is an agent's full, ordered escalation policy.
type Ladder []LadderRung

// defaultRelayTimeout bounds a relay_to_role/surface_to_human rung that
// declares no explicit timeout.
//
// 24 HOURS, not a short human-SLA-shaped number: the 2026-07-24
// approval-drop cluster was children auto-DENIED after a 5-minute default
// while a busy COORDINATOR AGENT — not a human at a terminal — simply
// hadn't called agent_recv yet. A coordinator can legitimately be deep in
// its own multi-hour turn before it drains its mailbox, so a relay rung
// must wait for a real answer rather than silently declining out from
// under it. Agent-configurable: an agent's own escalation: block can set a
// per-rung Timeout (agents.EscalationRung.Timeout, Go duration syntax) that
// overrides this default for THAT agent (buildLadder parses it below); this
// constant is only what an agent with no escalation: config — or a
// relay/surface rung that omits Timeout — gets.
const defaultRelayTimeout = 24 * time.Hour

// approvalKindNames is the user-facing short-form vocabulary for
// ApprovalRequest.ApprovalKind — deliberately hand-written (not derived from
// the proto's enum-value reflection) so agent YAML stays readable and
// independent of the wire's APPROVAL_KIND_ prefix.
var approvalKindNames = map[string]agentcoordpb.ApprovalRequest_ApprovalKind{
	"COMMAND_EXECUTION":     agentcoordpb.ApprovalRequest_APPROVAL_KIND_COMMAND_EXECUTION,
	"FILE_CHANGE":           agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE,
	"TOOL_USE":              agentcoordpb.ApprovalRequest_APPROVAL_KIND_TOOL_USE,
	"PERMISSION_ESCALATION": agentcoordpb.ApprovalRequest_APPROVAL_KIND_PERMISSION_ESCALATION,
	"ARTIFACT_REVIEW":       agentcoordpb.ApprovalRequest_APPROVAL_KIND_ARTIFACT_REVIEW,
	"CUSTOM":                agentcoordpb.ApprovalRequest_APPROVAL_KIND_CUSTOM,
}

// unrecognisedApprovalKind is the fail-closed placeholder ladderFromFact
// substitutes for a journaled kind name this build's approvalKindNames does
// not cover — a rung written by a LATER ctxloom that added an ApprovalKind.
//
// It exists because dropping such a name was a privilege escalation:
// a rung whose only kind was unrecognised came back with
// Kinds == nil, and matches() reads an empty Kinds list as a CATCH-ALL, so an
// auto_accept rung meant for one narrow kind replayed as "auto-accept
// everything". Keeping a value in the list preserves "this rung is targeted";
// choosing one outside the proto's value space guarantees it answers for
// nothing. Negative is safe: proto enum values are non-negative, so no real
// ApprovalKind can ever collide with it, now or later.
const unrecognisedApprovalKind = agentcoordpb.ApprovalRequest_ApprovalKind(-1)

// presetLadder derives the degenerate ladder the existing permissions:
// enum implies (the enum keeps working unchanged for
// every agent that declares no explicit escalation:):
//
//   - bypass: auto-accept every kind — mirrors decidePermission's old
//     "allow under bypass" default, now expressed as a one-rung ladder.
//   - plan: auto-decline the kinds that mutate state OUTRIGHT (file
//     changes, permission escalation) and relay everything else —
//     including COMMAND_EXECUTION, which is inherently ambiguous (a
//     command can be as read-only as `ls` or as destructive as `rm -rf`)
//     and so gets a judgment call rather than a reflexive decline.
//   - default/acceptEdits: UNREACHABLE for a delegated child in practice —
//     headlessSafePermission (spawner.go) already refuses them at spawn
//     (D3's structural floor is unchanged by this ladder). The decline-all
//     ladder here is a defensive fallback only, never the live path.
func presetLadder(perm agent.PermissionMode) Ladder {
	switch perm {
	case agent.PermissionBypass:
		return Ladder{{Action: ActionAutoAccept}}
	case agent.PermissionPlan:
		return Ladder{
			{
				Kinds: []agentcoordpb.ApprovalRequest_ApprovalKind{
					agentcoordpb.ApprovalRequest_APPROVAL_KIND_FILE_CHANGE,
					agentcoordpb.ApprovalRequest_APPROVAL_KIND_PERMISSION_ESCALATION,
				},
				Action: ActionAutoDecline,
			},
			{Action: ActionRelayToRole, Role: ParentAddress, Timeout: defaultRelayTimeout},
		}
	default:
		return Ladder{{Action: ActionAutoDecline}}
	}
}

// buildLadder validates and converts an agent's raw declared escalation:
// block into a Ladder. Empty raw derives the preset from perm (the common
// case: most agents never write an escalation: block). A non-empty raw
// REPLACES the preset entirely — no merge — so an agent that customizes
// escalation owns the whole ladder, including its own bottom.
func buildLadder(agentName string, raw []agents.EscalationRung, perm agent.PermissionMode) (Ladder, error) {
	if len(raw) == 0 {
		return presetLadder(perm), nil
	}
	out := make(Ladder, 0, len(raw))
	for i, r := range raw {
		rung := LadderRung{Action: LadderAction(r.Action)}
		switch rung.Action {
		case ActionAutoAccept, ActionAutoDecline:
			// Both fields are meaningless on a rung that resolves the request
			// immediately, and both are refused for the same reason: a rung
			// that cannot honour what the operator wrote must say so rather
			// than build a ladder that silently differs from the config.
			if r.Role != "" {
				return nil, fmt.Errorf("agent %q escalation[%d]: role is not meaningful for action %q", agentName, i, r.Action)
			}
			if r.Timeout != "" {
				return nil, fmt.Errorf("agent %q escalation[%d]: timeout is not meaningful for action %q — it resolves the request immediately; a timeout only bounds %q/%q",
					agentName, i, r.Action, ActionRelayToRole, ActionSurfaceToHuman)
			}
		case ActionRelayToRole, ActionSurfaceToHuman:
			role := r.Role
			if role == "" {
				role = ParentAddress
			}
			if role != ParentAddress {
				return nil, fmt.Errorf("agent %q escalation[%d]: role %q is not addressable — only %q is reachable in this window (flat-hub topology)", agentName, i, role, ParentAddress)
			}
			rung.Role = role
			rung.Timeout = defaultRelayTimeout
			if r.Timeout != "" {
				d, err := time.ParseDuration(r.Timeout)
				if err != nil {
					return nil, fmt.Errorf("agent %q escalation[%d]: timeout %q: %w", agentName, i, r.Timeout, err)
				}
				if d <= 0 {
					return nil, fmt.Errorf("agent %q escalation[%d]: timeout %q must be positive", agentName, i, r.Timeout)
				}
				rung.Timeout = d
			}
		default:
			return nil, fmt.Errorf("agent %q escalation[%d]: unknown action %q (known: %s|%s|%s|%s)",
				agentName, i, r.Action, ActionAutoAccept, ActionAutoDecline, ActionRelayToRole, ActionSurfaceToHuman)
		}
		for _, k := range r.Kinds {
			kind, ok := approvalKindNames[k]
			if !ok {
				return nil, fmt.Errorf("agent %q escalation[%d]: unknown kind %q (known: %s)", agentName, i, k, knownKindNames())
			}
			rung.Kinds = append(rung.Kinds, kind)
		}
		out = append(out, rung)
	}
	return out, nil
}

// knownKindNames renders approvalKindNames' keys for an error message —
// order is irrelevant (diagnostic text only), so no sort dependency.
func knownKindNames() string {
	s := ""
	for k := range approvalKindNames {
		if s != "" {
			s += "|"
		}
		s += k
	}
	return s
}

// matches reports whether rung answers kind: an empty Kinds list is a
// catch-all; otherwise kind must appear in it.
func (r LadderRung) matches(kind agentcoordpb.ApprovalRequest_ApprovalKind) bool {
	if len(r.Kinds) == 0 {
		return true
	}
	for _, k := range r.Kinds {
		if k == kind {
			return true
		}
	}
	return false
}

// matchingRungs returns the ladder's rungs matching kind, in ladder order —
// the walk serveApproval performs, one rung at a time, falling through on
// timeout to the next entry this returns.
func (l Ladder) matchingRungs(kind agentcoordpb.ApprovalRequest_ApprovalKind) []LadderRung {
	var out []LadderRung
	for _, r := range l {
		if r.matches(kind) {
			out = append(out, r)
		}
	}
	return out
}

// approvalKindName is approvalKindNames' inverse (for durable/audit
// serialization — kind NAMES, not wire numbers, so the journals stay
// jq-legible). Falls back to the wire enum's own String() for a kind this
// package's short-form table doesn't cover (forward compatibility with a
// later-added ApprovalKind).
func approvalKindName(k agentcoordpb.ApprovalRequest_ApprovalKind) string {
	for name, v := range approvalKindNames {
		if v == k {
			return name
		}
	}
	return k.String()
}

// ladderToFact projects a Ladder onto its durable fact shape.
func ladderToFact(l Ladder) []ladderRungFact {
	if len(l) == 0 {
		return nil
	}
	out := make([]ladderRungFact, 0, len(l))
	for _, r := range l {
		f := ladderRungFact{Action: string(r.Action), Role: r.Role}
		if r.Timeout > 0 {
			f.Timeout = r.Timeout.String()
		}
		for _, k := range r.Kinds {
			f.Kinds = append(f.Kinds, approvalKindName(k))
		}
		out = append(out, f)
	}
	return out
}

// ladderFromFact is ladderToFact's inverse. An unparseable timeout is dropped
// rather than failing replay: a fold must never error on its own store's
// history.
//
// An unrecognised kind name is NOT dropped. Dropping it: the rung
// came back with an empty Kinds list, which matches() reads as a catch-all, so
// a narrowly-targeted auto_accept rung journaled by a later ctxloom replayed
// as "auto-accept everything". It is replaced by unrecognisedApprovalKind
// instead, which keeps the rung TARGETED and answers for nothing — a rung this
// build cannot understand is a rung this build must not act on.
func ladderFromFact(facts []ladderRungFact) Ladder {
	if len(facts) == 0 {
		return nil
	}
	out := make(Ladder, 0, len(facts))
	for _, f := range facts {
		r := LadderRung{Action: LadderAction(f.Action), Role: f.Role}
		if f.Timeout != "" {
			if d, err := time.ParseDuration(f.Timeout); err == nil {
				r.Timeout = d
			}
		}
		for _, k := range f.Kinds {
			if kind, ok := approvalKindNames[k]; ok {
				r.Kinds = append(r.Kinds, kind)
				continue
			}
			clidiag.Warn("ctxloom", "coordinator: journaled approval kind %q is not one this build understands "+
				"(%s); the ladder rung is kept but will never match — it was written by a newer ctxloom", k, knownKindNames())
			r.Kinds = append(r.Kinds, unrecognisedApprovalKind)
		}
		out = append(out, r)
	}
	return out
}
