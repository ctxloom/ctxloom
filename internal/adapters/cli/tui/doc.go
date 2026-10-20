// Package tui is the split view of an interactive `ctxloom run`: the agent
// tree on the left, and on the right the selected agent — the session's own
// engine live in one emulated terminal (internal/adapters/termemu), a
// delegated child as its event feed above an input line, or the approvals
// view. One owner draws all of it (Split, started by StartSplit), so nothing
// is layered over a passthrough.
//
// All data arrives through the frontend port (internal/core/frontend) — the
// session index, the coordinator's runs, one harp's feed — injected by the
// CLI wiring and faked in tests (frontendfake), so the model is hermetically
// testable through Update without a terminal; it never holds a coordinator.
//
// Control of a viewed agent rides one seam, the port's Control: the tree's
// keys start a verb (controlKeys), a verb that carries text latches the
// child's input line, and the round trip's outcome is the panel's note.
//
// Approvals ride the port's Approvals, the root's coord.ApprovalSource: the
// approvals view lists the parked requests and is the only place one is
// answered. A presenter summons it (SplitView.Summon) or the human opens it
// from the tree; a summon takes the pane only at a pause in typing and is
// inert while it arms (PresentPolicy), its keymap is the fourth focus lock
// (see approvalsModel), and every child-supplied string it shows passes
// displaysafe.Text.
package tui
