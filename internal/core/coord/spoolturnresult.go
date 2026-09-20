package coord

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/spool"
)

// THE RESULT PLANE: a child's automatic turn report is written by ITS OWN
// RUNNER, into its own out/ spool, and routed to the parent exactly like any
// other message the child sends. The coordinator composes no report of its
// own: a file in a child's out/ may only be written by that child's runner
// (single writer per direction, §1.1 — the invariant that makes ordering and
// the consume-rename trivial).
//
// Three properties this file exists to hold:
//
//   - EXACTLY ONCE. The runner writes one report per turn, and the
//     coordinator never adds a second.
//   - A SELF-REPORT SUPPRESSES IT. The automatic report is a FALLBACK: a
//     child that called agent_send to its parent during the turn has already
//     reported in its own words. That check happens HERE, where the send is
//     (Home sees every agent_send this run makes).
//   - AN EMPTY TURN IS STILL REPORTED, AS AN ERROR. A turn with no output and
//     no self-report produced nothing to deliver, and the parent — an agent
//     whose sole input is its mail — must not simply hear nothing; the file
//     goes where the parent looks.
//
// CORRELATION: when the turn was started by a delivered mail file, the report
// quotes that message's id in in_reply_to. A parent that sent three children
// the same question can tell which answer answers which ask, without a
// convention.

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

// autoReportStructured is the marker payload, written as literal JSON rather
// than marshalled: it is one constant object, and a literal cannot acquire a
// field by accident.
func autoReportStructured() json.RawMessage { return json.RawMessage(`{"` + autoReportKey + `":true}`) }

// isAutoReport reports whether structured marks this message as an automatic
// turn report. Anything that is not an object with that key set to true is
// not one — an unparsable payload is emphatically not a reason to grant the
// exemption.
func isAutoReport(structured json.RawMessage) bool {
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

// ReportTurnResult is the runner half of the automatic turn report: it writes
// this turn's own output into this run's out/ spool as `kind: result`,
// correlated to the message that started the turn.
//
// It is a NO-OP for the session owner's own run (depth 0): that run has no
// parent to report to — the host watches it directly — and a report written
// to "parent" would come back as a refusal, delivered as the run's next turn,
// which reports again: an infinite self-loop.
//
// text is the turn's FINAL-channel output, already joined by the caller
// (EngineHost accumulates the same deltas, in the same order, that the
// coordinator's accumulator did). inReplyTo is the id of the delivered
// message that started the turn, or empty for a turn nothing delivered
// started — a briefing, or an engine continuing on its own.
func (h *Home) ReportTurnResult(text, inReplyTo string) error {
	if h.Depth() == 0 {
		return nil
	}
	if h.takeSelfReported() {
		// The child already reported, in its own words. Never deliver one
		// turn twice.
		return nil
	}
	body := strings.TrimSpace(text)
	kind := KindResult
	if body == "" {
		// An empty body is this project's signature silent no-op, not a
		// report — so this is not written as an empty result. It is written as
		// an ERROR the parent can act on, which is the whole point: under a
		// prompt-delivery defect this fires every turn while roster state,
		// transcript existence and exit code all stay green.
		kind = KindError
		body = fmt.Sprintf("agent %q (run %s) turn produced no output — nothing to report", h.Harp(), h.cfg.RunID)
		h.rep.Warnf("runner: this turn ended with no report and no output; telling the parent so (%s)", h.Harp())
	}
	if _, err := h.writeOutbound(Message{
		From: h.Harp(), To: ParentAddress, Kind: kind, Body: body, InReplyTo: inReplyTo,
		// MARKED AUTOMATIC. The correlation above is what makes this necessary:
		// without the marker this message is indistinguishable from the child
		// deliberately answering the ask that started the turn.
		Structured: autoReportStructured(),
	}); err != nil {
		// LOUD AND COUNTED. A report that could not be written is a turn the
		// parent will never hear about, and the accumulator that held it has
		// already been taken — there is nothing to retry from, so the failure
		// is the only trace and it must exist.
		h.rep.Warnf("runner: could not write this turn's report for %s: %v (the parent will not hear about this turn)", h.Harp(), err)
		h.spoolDeliveryCount.failed.Add(1)
		return err
	}
	h.spoolDeliveryCount.delivered.Add(1)
	return nil
}

// noteSelfReported records that this run sent its parent a message during the
// current turn, so the automatic report does not repeat it.
//
// It is set at the SEND, not at the turn boundary, because that is the only
// place the fact exists: by the boundary the send is indistinguishable from
// any other completed request.
func (h *Home) noteSelfReported() {
	h.mu.Lock()
	h.selfReported = true
	h.mu.Unlock()
}

// takeSelfReported reads and clears the flag — one turn's suppression never
// carries into the next, or a child that reported once would go silent for the
// rest of the run.
func (h *Home) takeSelfReported() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	was := h.selfReported
	h.selfReported = false
	return was
}

// writeOutbound publishes one message into THIS run's out/ spool and rings the
// coordinator.
//
// It is the single writer of this direction, shared by agent_send and the
// automatic turn report so the two cannot diverge in what a file looks like:
// the same projection, the same writer (and therefore the same sequence
// counter — two writers for one directory could mint one filename twice), and
// the same fire-and-forget doorbell, whose failure costs a sweep interval and
// never a message.
func (h *Home) writeOutbound(msg Message) (spool.Ref, error) {
	// Write-and-ring is ONE operation (spoolcourier.go). This end and the
	// coordinator's differ only in which writer set they own, which harp keys
	// it, how they ring, and whether the send is audited — everything else was
	// the same prose in two files.
	return h.outboundCourier().Send(msg)
}

// outboundCourier writes into THIS RUN's outbound spool, whoever the message is
// addressed to — the runner owns one spool, not one per recipient.
func (h *Home) outboundCourier() *spoolCourier {
	return &spoolCourier{
		rep:     h.rep,
		writers: h.spoolOut,
		keyFor:  func(string) string { return h.Harp() },
		ring:    func(_ string, ref spool.Ref) error { return h.ringSpool(ref) },
		side:    "runner",
	}
}
