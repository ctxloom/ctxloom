package coord

import (
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/encoding/protojson"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/agentcoord/spool"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The SHADOW TEE: every mailbox delivery, additionally written as a spool file
// and announced with a doorbell, while every READ still comes from the mailbox.
//
// The tee is off by default (config delegation.spool_tee) and changes no
// delivery behaviour when it is on. That is the whole point: the file
// substrate has to carry real production traffic — real kinds, real
// correlation ids, real structured payloads — before anything reads from it,
// because the only way to learn that the mailbox->file mapping loses a field
// is to write the files and compare. Doing that comparison AFTER a cutover
// means discovering it as lost mail.
//
// Four rules hold everywhere in this file:
//
//   - THE MAILBOX IS STILL THE SYSTEM OF RECORD. A tee failure — an
//     unwritable spool root, a kind with no mapping, a structured payload that
//     will not project — warns loudly, is counted, and RETURNS. It never fails
//     the delivery it shadows. A shadow that can break the thing it shadows is
//     worse than no shadow.
//   - LOUD, NOT SILENT. Every failure increments a counter an observer can
//     read (SpoolTeeStats) as well as warning, because a systematic mapping
//     gap that only logs reads as "the spool looks a bit empty" for as long as
//     nobody counts.
//   - SENDER IDENTITY IS THE DIRECTORY. The coordinator writes in/ and only
//     in/; a runner writes its own harp's out/ and only that. The frontmatter
//     from_harp is advisory display metadata, exactly as spool.Message says.
//     Single writer per direction is what makes ordering and the consume
//     rename trivial later.
//   - NOTHING HAPPENS WHEN THE FLAG IS OFF. No writer is constructed, so no
//     spool directory is created and no doorbell is rung. The directory's
//     ABSENCE is what a test asserts to prove inertness — a check that a
//     lazily-created writer would quietly defeat.
//
// One consequence worth stating because it looks like double-teeing and is
// not: when a child's message is addressed to a parent that is ITSELF a
// tracked run (a container top-level session, StartOwnedRun), two files
// appear — the child's runner writes the child's out/, and the coordinator
// writes the parent's in/. That is one relay hop rendered as its two halves,
// which is precisely how the post-cutover design routes between two
// runner-backed sessions. A parent that is the coordinator's own in-process
// mailbox has no run record and gets no in/ file.

// spoolWriterIDCoordinator is the writer token stamped into every filename the
// COORDINATOR publishes. A fixed token, not a harp: in/ has exactly one writer
// whichever session's spool it is, and naming it after the recipient would make
// every filename claim the wrong author.
const spoolWriterIDCoordinator = "coord"

// SpoolTeeStats reports what the shadow tee did and failed to do. Both
// counters are cumulative for the process's lifetime.
type SpoolTeeStats struct {
	// Written counts mail successfully mirrored into the spool.
	Written uint64
	// Failed counts mail the tee could NOT mirror — an unmappable kind, a
	// structured payload that would not project, a spool write that errored.
	// Every one of them is a delivery whose file twin does not exist, which is
	// exactly the divergence the soak is looking for.
	Failed uint64
}

// spoolTeeCounters is the shared counter pair, embedded by both ends.
type spoolTeeCounters struct {
	written atomic.Uint64
	failed  atomic.Uint64
}

func (s *spoolTeeCounters) stats() SpoolTeeStats {
	return SpoolTeeStats{Written: s.written.Load(), Failed: s.failed.Load()}
}

// spoolWriterCache lends one spool.Writer per harp for ONE direction.
//
// Writers are cached rather than made per message because spool.NewWriter
// re-seeds its sequence by reading the whole direction plus its consumed and
// withdrawn siblings: correct per call, but O(mailbox) per message, and two
// writers for one directory would also each hold their own sequence counter
// and could mint the same filename inside one nanosecond. One writer per
// (harp, direction) is what makes spool.Writer's own mutex sufficient.
type spoolWriterCache struct {
	mapper spool.PathMapper
	dir    spool.Dir
	id     string

	mu      sync.Mutex
	writers map[string]*spool.Writer
}

func newSpoolWriterCache(m spool.PathMapper, dir spool.Dir, writerID string) *spoolWriterCache {
	return &spoolWriterCache{mapper: m, dir: dir, id: writerID, writers: map[string]*spool.Writer{}}
}

// writerFor returns harp's writer, creating (and thereby creating the spool
// directories) on first use. The lock is held across construction — which does
// filesystem work — deliberately: it happens once per harp, and letting two
// callers race to build writers for one directory is how the duplicate
// sequence counter above gets created.
func (c *spoolWriterCache) writerFor(harp string) (*spool.Writer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w, ok := c.writers[harp]; ok {
		return w, nil
	}
	w, err := spool.NewWriter(c.mapper, harp, c.dir, c.id)
	if err != nil {
		return nil, err
	}
	c.writers[harp] = w
	return w, nil
}

// spoolMessageForMail projects one mailbox Message onto its spool.Message.
//
// This function IS the mailbox->file mapping, and every field it drops would
// be a field the post-cutover system never had. What maps:
//
//	mailbox.Kind       -> frontmatter kind      (SpoolKindForMail; "" is a kind)
//	mailbox.ID         -> frontmatter origin_id (the mailbox's own id; spool.Message.ID
//	                      is the filename stem and is not a producer's to choose)
//	mailbox.From       -> frontmatter from_harp (ADVISORY — the directory is identity)
//	mailbox.To         -> frontmatter to        (advisory in the same way)
//	mailbox.InReplyTo  -> frontmatter in_reply_to
//	mailbox.Structured -> frontmatter structured (spoolStructured; a payload
//	                      that is not a JSON object rides the wrapper key —
//	                      see spoolRawJSONKey)
//	mailbox.Body       -> the markdown body, verbatim
//
// Nothing else exists on a mailbox Message, and the mapping is total: a kind
// outside the closed vocabulary and a structured payload that is not a JSON
// object are both refused here, at the projection, rather than written as a
// file missing the part that would not fit.
func spoolMessageForMail(msg Message, to string) (*spool.Message, error) {
	kind, err := SpoolKindForMail(msg.Kind)
	if err != nil {
		return nil, err
	}
	structured, err := spoolStructured(msg.Structured)
	if err != nil {
		return nil, err
	}
	return &spool.Message{
		Kind:       kind,
		FromHarp:   msg.From,
		To:         to,
		InReplyTo:  msg.InReplyTo,
		OriginID:   msg.ID,
		Structured: structured,
		Body:       msg.Body,
	}, nil
}

// ---- coordinator side --------------------------------------------------

// SpoolTeeEnabled reports whether this coordinator mirrors mail into the spool.
func (c *Coordinator) SpoolTeeEnabled() bool { return c.spoolTee }

// SpoolTeeStats reports this coordinator's cumulative tee outcomes.
func (c *Coordinator) SpoolTeeStats() SpoolTeeStats { return c.spoolTeeCount.stats() }

// teeMailToRun mirrors one committed mailbox delivery into the recipient's in/
// spool and rings that recipient's runner.
//
// It is called AFTER the durable queue fact is written — the moment the
// coordinator has actually committed the mail — and never returns an error:
// the caller's delivery must not be able to fail because a shadow did.
//
// Mail addressed to something that is not a tracked RUN is not teed at all:
// the session owner's own mailbox is drained in this process by AgentRecv,
// there is no runner and no spool on the other end of it, and inventing a
// directory for it would put files somewhere nothing will ever sweep.
func (c *Coordinator) teeMailToRun(to string, msg Message) {
	if !c.spoolTee {
		return
	}
	tracked := false
	c.runs.View(func() { tracked = c.runsF.currentRun(to) != nil })
	if !tracked {
		return
	}
	sm, err := spoolMessageForMail(msg, to)
	if err != nil {
		c.noteSpoolTeeFailure(to, msg.ID, err)
		return
	}
	w, err := c.spoolIn.writerFor(to)
	if err != nil {
		c.noteSpoolTeeFailure(to, msg.ID, err)
		return
	}
	ref, err := w.Write(sm)
	if err != nil {
		c.noteSpoolTeeFailure(to, msg.ID, err)
		return
	}
	c.spoolTeeCount.written.Add(1)
	// Fire-and-forget by contract (RingSpool): a doorbell that cannot go out
	// costs latency, never a message, because the file is already on disk. The
	// only error it can return is an unusable ref, which means the writer just
	// produced one — worth the same loud failure as a failed write.
	if err := c.RingSpool(to, ref); err != nil {
		clidiag.Warn("ctxloom", "coordinator: spool tee wrote %s but could not ring it: %v", ref, err)
	}
}

// noteSpoolTeeFailure reports and then counts, in that order — the same
// ordering discipline noteSpoolDrop documents: incrementing LAST is what makes
// "the count moved" imply "the report is already written".
func (c *Coordinator) noteSpoolTeeFailure(to, msgID string, err error) {
	clidiag.Warn("ctxloom", "coordinator: spool tee could not mirror message %s to %s: %v (the mailbox delivery itself is unaffected; the spool now diverges from it by one message)", msgID, to, err)
	c.spoolTeeCount.failed.Add(1)
}

// ---- runner side -------------------------------------------------------

// SpoolTeeEnabled reports whether this runner mirrors its outbound mail into
// the spool. It answers from the WRITER, not from the config bit: a tee
// configured on for a run with no harp has no spool to write into, and
// reporting it as enabled would make the counters look like the mirror is
// running when nothing is being mirrored.
func (h *Home) SpoolTeeEnabled() bool { return h.spoolOut != nil }

// SpoolTeeStats reports this runner's cumulative tee outcomes.
func (h *Home) SpoolTeeStats() SpoolTeeStats { return h.spoolTeeCount.stats() }

// teePeerSendResponse mirrors an ACCEPTED agent_send into this harp's out/
// spool, and ignores every other plane-2 request and every refusal.
//
// It reads the request and the coordinator's answer TOGETHER because neither
// alone carries the whole message: the text, recipient, correlation and
// structured payload are the request's, while the mailbox id — the only handle
// that ties this file to its mailbox twin — is minted coordinator-side and
// comes back on the response.
//
// The decoding here deliberately mirrors servePeerSend's, field for field
// (recipient from to_agent_id OR to_role, kind read out of structured.kind
// while structured itself is carried whole). A second, subtly different
// reading of the same request would make the tee's files disagree with the
// mailbox about what was sent — which is the one thing a shadow must never do.
func (h *Home) teePeerSendResponse(req *agentcoordpb.AgentRequest, resp *agentcoordpb.CoordinatorResponse) {
	if !h.SpoolTeeEnabled() {
		return
	}
	send := req.GetPeerSend()
	if send == nil {
		return
	}
	if code := resp.GetStatus().GetCode(); code != int32(codes.OK) {
		return
	}
	result := resp.GetPeerSend()
	if result == nil {
		// An OK status with no PeerSendResult is not a send this runner can
		// describe: without the coordinator's message id the file would have
		// no traceable twin. Counted, because it means the two ends disagree
		// about the response shape.
		h.noteSpoolTeeFailure("", fmt.Errorf("coord: agent_send answered OK with no PeerSendResult, so the mailbox id is unknown"))
		return
	}
	to := send.GetToAgentId()
	if role := send.GetToRole(); role != "" {
		to = role
	}
	kind := ""
	var structured json.RawMessage
	if s := send.GetStructured(); s != nil {
		if v, ok := s.GetFields()["kind"]; ok {
			kind = v.GetStringValue()
		}
		raw, err := protojson.Marshal(s)
		if err != nil {
			h.noteSpoolTeeFailure(result.GetMessageId(), fmt.Errorf("coord: re-encoding the sent structured payload for the spool: %w", err))
			return
		}
		structured = raw
	}
	h.teeMailFromAgent(result.GetMessageId(), to, kind, send.GetText(), send.GetInReplyTo(), structured)
}

// teeMailFromAgent mirrors one accepted outbound agent_send into THIS harp's
// out/ spool and rings the coordinator.
//
// It runs after the coordinator ACCEPTED the send, so a refused send leaves no
// phantom file: out/ is the record of what this agent actually sent, and a
// message the coordinator rejected was never sent. msgID is the mailbox id the
// coordinator minted, which is what makes this file matchable against its
// mailbox twin (spool.Message.OriginID).
//
// Like the coordinator's half it never returns an error: the agent's send has
// already succeeded and must not be retro-actively failed by a shadow.
func (h *Home) teeMailFromAgent(msgID, to, kind, body, inReplyTo string, structured json.RawMessage) {
	if !h.SpoolTeeEnabled() {
		return
	}
	sm, err := spoolMessageForMail(Message{
		ID: msgID, From: h.cfg.Harp, To: to, Kind: kind,
		Body: body, Structured: structured, InReplyTo: inReplyTo,
	}, to)
	if err != nil {
		h.noteSpoolTeeFailure(msgID, err)
		return
	}
	w, err := h.spoolOut.writerFor(h.cfg.Harp)
	if err != nil {
		h.noteSpoolTeeFailure(msgID, err)
		return
	}
	ref, err := w.Write(sm)
	if err != nil {
		h.noteSpoolTeeFailure(msgID, err)
		return
	}
	h.spoolTeeCount.written.Add(1)
	if err := h.RingSpool(ref); err != nil {
		clidiag.Warn("ctxloom", "runner: spool tee wrote %s but could not ring it: %v", ref, err)
	}
}

// noteSpoolTeeFailure reports and then counts — see the coordinator's twin.
func (h *Home) noteSpoolTeeFailure(msgID string, err error) {
	clidiag.Warn("ctxloom", "runner: spool tee could not mirror outbound message %s: %v (the send itself succeeded; the spool now diverges from the mailbox by one message)", msgID, err)
	h.spoolTeeCount.failed.Add(1)
}
