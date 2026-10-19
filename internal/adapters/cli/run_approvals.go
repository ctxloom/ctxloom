package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/adapters/cli/tui"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// presentsApprovals reports whether a run's terminal will present approvals
// to the human. The modal on the terminal layer is the queue's one
// presenter, so these are the facts that decide whether prepareSessionIO
// builds that layer, and marks the queue unattended when it does not: an
// interactive launch (a one-shot has no terminal session), no
// --plain-terminal, stdin a terminal, and a prefix key setupTerminalUI can
// parse (refused at the startup gate unless --degraded).
func presentsApprovals(mode engine.Mode, plainTerminal, stdinTerminal bool, prefixErr error) bool {
	return mode == engine.Interactive && !plainTerminal && stdinTerminal && prefixErr == nil
}

// approvalsPresented is presentsApprovals for this run, known at startup —
// before the coordinator is hosted or the terminal layer built — so the
// warnings can say what the queue will do with its requests.
func (st *runState) approvalsPresented() bool {
	_, prefixErr := tui.ParsePrefixKey(st.cfg.UIPrefixKey())
	return presentsApprovals(st.launch.Mode, runPlainTerminal, termIsTerminal(int(os.Stdin.Fd())), prefixErr)
}

// postureWarning is what a run says at startup about the gated calls its
// resolved policy will not let through, or "" when there is nothing to say.
// Only a one-shot root is warned: an interactive root's own asks are
// answered in the engine's UI, and what a child of a root that presents
// nothing would ask is the startup finding's (unpresentedApprovalFinding).
// The engine's posture that asks nobody (asksNobody) is silent, and a
// reviewer decides on its own. Approver none denies at once; so does the
// human where nothing presents approvals (the root's queue is unattended,
// coord.ApprovalQueue.Unattended), and then not only the run's own asks but
// every one its children put to the human, which park in its queue.
func postureWarning(mode engine.Mode, p engine.PermissionPolicy, model engine.Declared[engine.PermissionModel], posture string, presented bool) string {
	if mode != engine.Structured || asksNobody(model, posture) {
		return ""
	}
	switch {
	case p.Approver == engine.ApproverNone:
		return fmt.Sprintf("--one-shot with %s permissions and approver none: gated calls are denied at once, so those steps will not run", posture)
	case p.Approver == engine.ApproverHuman && !presented:
		return fmt.Sprintf("--one-shot with %s permissions: no one can answer approvals in this run, so gated calls are denied at once — this run's, and those any delegated child puts to a human; set a permissions mode or a reviewer approver", posture)
	}
	return ""
}

// asksNobody reports whether posture, in its engine's words, is the one that
// engine runs at asking nobody (engine.PermissionModel.Ungated). Each engine
// spells it differently, so only its own model can say; an engine that has
// no such posture, or no permission model, never asks nobody.
func asksNobody(model engine.Declared[engine.PermissionModel], posture string) bool {
	m, ok := model.Get()
	if !ok {
		return false
	}
	ungated, ok := m.Ungated()
	return ok && posture == ungated
}

// permissionModelOf is the named engine's permission model; absent for an
// engine reg does not hold.
func permissionModelOf(reg engine.Registry, name string) engine.Declared[engine.PermissionModel] {
	e, ok := reg.Lookup(engine.Name(name))
	if !ok {
		return engine.Absent[engine.PermissionModel]("engine " + name + " is not registered")
	}
	return e.Permissions()
}

// unpresentedApprovalFinding is the startup finding of a root that will not
// present approvals but may delegate to the agents in mayDelegate: its
// children's requests park in its unattended queue, so every call a child
// would put to the human is denied at once. None for a root that presents
// approvals, or that has nobody to delegate to.
func unpresentedApprovalFinding(presented bool, mayDelegate []string) (report.Finding, bool) {
	if presented || len(mayDelegate) == 0 {
		return report.Finding{}, false
	}
	return report.Finding{
		Text: fmt.Sprintf("no one can answer approvals in this run, so a delegated child's gated calls are denied at once rather than asked (agents this run may delegate to: %s)",
			strings.Join(mayDelegate, ", ")),
		Remedy: "give those agents a permissions mode that covers their work (ctxloom agent edit <agent> --permissions <posture>) or approver: reviewer, or run interactively on a terminal without --plain-terminal to answer them",
	}, true
}

// delegable is the agents this run may delegate to: its binding's
// may_delegate, or — when that lists none, which admits any — every
// configured agent (the rule is coord.Coordinator.admitDelegation's).
func (st *runState) delegable() []string {
	if names := st.mayDelegate(); len(names) > 0 {
		return names
	}
	var names []string
	for _, a := range st.cfg.LoadAgents() {
		names = append(names, a.Name)
	}
	return names
}
