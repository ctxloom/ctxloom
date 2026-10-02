package coord

import (
	"encoding/json"
	"errors"
)

// Typed mail refusals.
var (
	// ErrPeerRouting rejects a child addressing anyone off its tree edges: not
	// its own parent and not one of its own children.
	ErrPeerRouting = errors.New(`agent_send: a delegated session may address only its own parent ("parent") or its own children (by harp); reach any other agent through its parent`)
	// ErrRevoked refuses a call whose run credential was revoked (run ended /
	// agent_stop): a report recorded after the run ended is refused with it
	// (runsFold.liveRun), however valid the credential was when the call
	// started.
	ErrRevoked = errors.New("this session's credential was revoked")
)

// ParentAddress is the one recipient a spawned child may address: its own
// coordinator's session, resolved from journaled lineage.
const ParentAddress = "parent"

// UserSender is the sender identity on messages the human injects through the
// observation viewer — never a harp, so a recipient can tell a user message
// from its parent's.
const UserSender = "user"

// KindUserInjected marks the O3 mirror notice a user injection always sends
// to the target's parent.
const KindUserInjected = "user_injected"

// KindExited marks the synthesized terminal notice the coordinator queues to
// a parent when a child run ends (runner loss, chat-stream close, stop) —
// the orchestrator, and now the parent, always learns.
const KindExited = "exited"

// newMessageID mints a message id (the dedupe key).
func newMessageID() string { return RandID("m-", 12) }

// autoReportKey marks a message as the runner's AUTOMATIC turn report rather
// than something the agent chose to send. It rides the structured companion
// because the KIND must stay `result` — that is what the bridge's mailbox copy
// carried and what every parent already reads.
//
// It exists because the report now carries a CORRELATION, and correlation is
// authority: a message quoting an outstanding ask's id resolves that ask. An
// automatic report must not. The cooperative-reply ruling is that an ask is
// answered by what the child CHOSE to send; a report the runner composed from
// whatever the model happened to say is the involuntary capture that ruling
// excludes, and without this marker it would arrive through the back door
// wearing the right correlation.
//
// The marker only ever REMOVES authority from the message carrying it, never
// grants any, so a sender setting it on its own send can only decline to
// answer its own ask — which is not an attack, just a wasted send.
const autoReportKey = "auto_report"

// BlockedCall is one tool call a child's engine refused during the turn an
// automatic report covers — the report's "blocked" list.
type BlockedCall struct {
	Tool    string `json:"tool"`
	Reason  string `json:"reason,omitempty"`
	Decider string `json:"decider"`
}

// AutoReportStructured is the marker payload, plus the turn's refused calls
// when there were any: a turn that was blocked says so in the structure a
// parent can branch on, not only in prose.
func AutoReportStructured(blocked ...BlockedCall) json.RawMessage {
	payload := map[string]any{autoReportKey: true}
	if len(blocked) > 0 {
		payload["blocked"] = blocked
	}
	b, err := json.Marshal(payload)
	if err != nil {
		panic(err) // a bool and a slice of string structs always marshal
	}
	return b
}

// IsAutoReport reports whether structured marks this message as an automatic
// turn report. Anything that is not an object with that key set to true is
// not one — an unparsable payload is emphatically not a reason to grant the
// exemption.
func IsAutoReport(structured json.RawMessage) bool {
	if len(structured) == 0 {
		return false
	}
	var obj map[string]any
	if err := json.Unmarshal(structured, &obj); err != nil {
		return false
	}
	marked, _ := obj[autoReportKey].(bool)
	return marked
}
