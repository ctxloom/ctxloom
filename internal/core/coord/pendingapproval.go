package coord

import (
	"encoding/json"
	"time"
)

// PendingApproval is the shape the terminal UI's approvals pane reads.
//
// NOTHING PRODUCES ONE yet. A delegated child is headless: nobody answers
// its engine's prompts, so the engine denies what the child's posture leaves
// open and the runner reports that turn BLOCKED. No approval route reaches a
// human, so internal/adapters/cli/run_terminal_ui.go wires no pane and
// tui.Sources.PendingApprovals is nil — its documented "pane disabled" state.
type PendingApproval struct {
	MessageID string
	Harp      string // the run asking
	Kind      ApprovalKind
	Title     string // the tool name
	Payload   json.RawMessage
	Since     time.Time
	Deadline  time.Time
}
