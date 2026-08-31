package coord

import (
	"encoding/json"
	"time"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
)

// PendingApproval is the shape the terminal UI's approvals pane reads.
//
// NOTHING PRODUCES ONE. The orchestrator-routed escalation ladder that used
// to park approvals for a human decision is gone: ctxloom no longer brokers a
// second approval UI, because a human can attach to the agent's own tmux
// window and answer the ENGINE'S NATIVE prompt. The type survives only so
// internal/cli/tui and internal/termui keep compiling until they are deleted
// (that deletion is gated on `ctxloom attach` being proven in use); it goes
// with them. internal/cli/run_terminal_ui.go no longer wires the pane, so
// tui.Sources.PendingApprovals is nil — its documented "pane disabled" state.
//
// This is NOT the ACP permission relay, which is live and unrelated: an
// attached editor still handles session/request_permission via
// acpagent.forwardPermission.
type PendingApproval struct {
	MessageID string
	Harp      string // the run asking
	Kind      agentcoordpb.ApprovalRequest_ApprovalKind
	Title     string // the tool name
	Payload   json.RawMessage
	Since     time.Time
	Deadline  time.Time
}
