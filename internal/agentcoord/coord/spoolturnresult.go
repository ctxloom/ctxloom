package coord

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// THE RESULT PLANE'S CUTOVER: a child's automatic turn report is written by
// ITS OWN RUNNER, into its own out/ spool, and routed to the parent exactly
// like any other message the child sends.
//
// What moved, and why it had to. The report is not a new message — it is the
// bridge (children.go's bridgeTurnResult), which the COORDINATOR composed from
// plane-1 message events it had accumulated and queued as mailbox mail. That
// worked because the coordinator was the delivery substrate. Under the cutover
// it is not: the file is, and a file in a child's out/ may only be written by
// that child's runner (single writer per direction, §1.1 — the invariant that
// makes ordering and the consume-rename trivial). A coordinator writing a
// child's out/ to "bridge" would break it for the one message the child never
// chose to send.
//
// Three properties this file exists to hold:
//
//   - EXACTLY ONCE, FILE XOR BRIDGE. The runner writes the report only when
//     this run is cut over; the coordinator bridges only when it is not. The
//     two predicates are the same fact read from the two sides — the
//     coordinator's flag stamped onto the run at spawn — so a run cannot have
//     both or neither.
//   - A SELF-REPORT STILL SUPPRESSES IT. The bridge is a FALLBACK: a child that
//     called agent_send to its parent during the turn has already reported in
//     its own words. That check now happens HERE, where the send is (Home sees
//     every agent_send this run makes), instead of coordinator-side on
//     rt.selfReported.
//   - AN EMPTY TURN IS STILL REPORTED, AS AN ERROR. A turn with no output and
//     no self-report produced nothing to deliver, and the parent — an agent
//     whose sole input is its mail — must not simply hear nothing. The
//     coordinator's warn went to ITS stderr, which the parent cannot read; the
//     file goes where the parent looks.
//
// CORRELATION is the one thing the file carries that the mailbox bridge could
// not: when the turn was started by a delivered mail file, the report quotes
// that message's id in in_reply_to. A parent that sent three children the same
// question can tell which answer answers which ask, without a convention.

// autoReportKey marks a message as the runner's AUTOMATIC turn report rather
// than something the agent chose to send. It rides the structured companion
// because the KIND must stay `result` — that is what the bridge's mailbox copy
// carried and what every parent already reads.
//
// It exists because the report now carries a CORRELATION, and correlation is
// authority: a message quoting an outstanding ask's id resolves that ask, and a
// message quoting an approval's id is decoded as a decision. An automatic
// report must do NEITHER. The cooperative-reply ruling is that an ask is
// answered by what the child CHOSE to send; a report the runner composed from
// whatever the model happened to say is the involuntary capture that ruling
// excludes, and without this marker it would arrive through the back door
// wearing the right correlation.
//
// The marker only ever REMOVES authority from the message carrying it, never
// grants any, so a sender setting it on its own send can only decline to
// answer its own approval — which is not an attack, just a wasted send.
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
// It is a NO-OP for a run that is not cut over — the coordinator's bridge
// still owns the report there, unchanged — which is the whole of the
// exactly-once argument on this side.
//
// text is the turn's FINAL-channel output, already joined by the caller
// (EngineHost accumulates the same deltas, in the same order, that the
// coordinator's accumulator did). inReplyTo is the id of the delivered
// message that started the turn, or empty for a turn nothing delivered
// started — a briefing, or an engine continuing on its own.
func (h *Home) ReportTurnResult(text, inReplyTo string) error {
	if !h.spoolDelivery {
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
		body = fmt.Sprintf("agent %q (run %s) turn produced no output — nothing to report", h.cfg.Harp, h.cfg.RunID)
		clidiag.Warn("ctxloom", "runner: this turn ended with no report and no output; telling the parent so (%s)", h.cfg.Harp)
	}
	ref, err := h.writeOutbound(Message{
		From: h.cfg.Harp, To: ParentAddress, Kind: kind, Body: body, InReplyTo: inReplyTo,
		// MARKED AUTOMATIC. The correlation above is what makes this necessary:
		// without the marker this message is indistinguishable from the child
		// deliberately answering the ask that started the turn.
		Structured: autoReportStructured(),
	})
	if err != nil {
		// LOUD AND COUNTED. A report that could not be written is a turn the
		// parent will never hear about, and the accumulator that held it has
		// already been taken — there is nothing to retry from, so the failure
		// is the only trace and it must exist.
		clidiag.Warn("ctxloom", "runner: could not write this turn's report for %s: %v (the parent will not hear about this turn)", h.cfg.Harp, err)
		h.spoolDeliveryCount.failed.Add(1)
		return err
	}
	h.spoolDeliveryCount.delivered.Add(1)
	_ = ref
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
	sm, err := spoolMessageForMail(msg, msg.To)
	if err != nil {
		return spool.Ref{}, err
	}
	w, err := h.spoolOut.writerFor(h.cfg.Harp)
	if err != nil {
		return spool.Ref{}, fmt.Errorf("opening this run's outbound spool: %w", err)
	}
	ref, err := w.Write(sm)
	if err != nil {
		return spool.Ref{}, fmt.Errorf("writing the message: %w", err)
	}
	if rerr := h.RingSpool(ref); rerr != nil {
		clidiag.Warn("ctxloom", "runner: wrote %s but could not ring the coordinator: %v (it will be swept)", ref, rerr)
	}
	return ref, nil
}
