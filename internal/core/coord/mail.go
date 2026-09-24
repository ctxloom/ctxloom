package coord

import (
	"encoding/json"
	"errors"
)

// The owner's receive verb: agent_recv is the bounded long poll on the ONE
// inbox (spoolInbox) for the one recipient whose spool THIS PROCESS reads —
// the declared session owner. Every other recipient is a run whose own
// runner drains its spool (Home.Recv), so a receive here for any role but the
// owner is refused. The completions below name the event and nothing more:
// what the caller should do next depends on WHO is parked, which the MCP
// handlers know and attach.

// Typed completions. Neither the timeout nor the preemption is a fault, and
// what the caller should do next depends on WHO is parked — a child that
// times out finishes, a coordinator re-arms — which this package cannot
// know: recvMail sees a role, not an audience. So the sentinels name the
// event and nothing more; the MCP handlers, which do know their audience,
// attach the guidance.
var (
	// ErrRecvTimeout completes a parked agent_recv whose bounded wait
	// elapsed with no message.
	ErrRecvTimeout = errors.New("agent_recv: timed out with no message")
	// ErrPeerRouting rejects executor→executor addressing (hub-and-spoke).
	ErrPeerRouting = errors.New(`agent_send: executors may only address "parent"; route via coordinator`)
	// ErrRecvPreempted completes the OLDER of two long-polls for one role:
	// one active long-poll per role, newest preempts. It is a YIELD, not a
	// failure — no mail is lost, the newer poll holds the park — and the
	// tool surfaces render it as a successful empty receive; it rides the
	// error channel only because that is the one completion path a poll has.
	ErrRecvPreempted = errors.New("agent_recv: yielded to a newer receive for this session")
	// ErrRevoked refuses a call whose run credential was revoked (run ended /
	// agent_stop): revocation severs parked polls, and a receive or report
	// recorded after the run ended is refused with it (runsFold.liveRun),
	// however valid the credential was when the call started.
	ErrRevoked = errors.New("this session's credential was revoked")
	// ErrRecvNotOwner refuses a receive here for a role that is not the
	// declared session owner: that role's inbox is a spool its own runner
	// drains, and reading it from this process would either find nothing
	// or consume mail another process is about to deliver.
	ErrRecvNotOwner = errors.New("agent_recv: only the session owner receives at the coordinator; a run's runner drains its own spool")
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

// AutoReportStructured is the marker payload, written as literal JSON rather
// than marshalled: it is one constant object, and a literal cannot acquire a
// field by accident.
func AutoReportStructured() json.RawMessage { return json.RawMessage(`{"` + autoReportKey + `":true}`) }

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
