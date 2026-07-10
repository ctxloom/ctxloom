package coord

import (
	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Plane-1 ITEM journaling (Wave C1, per the pre-made journaling decision):
// item events — message/tool-call lifecycle, run started/completed, status —
// are contract-durable and journaled (items.jsonl), but with GROUP-FSYNC on
// the Ack watermark: delta events (message_delta, tool_call_args_delta — the
// storm kinds) buffer on the channel and flush in one append+fsync at the
// next BOUNDARY event (any non-delta) or when the buffer fills, and the
// cumulative plane-3 Ack advances only through FLUSHED (durable) seqs — the
// runner re-emits anything unflushed after a crash, deduped by the fold on
// (run, seq). Replay-equivalence folds COUNT items; delta text is never
// materialized (CHECKPOINT compaction is Wave D's job).

// factItem is one journaled plane-1 item event (counted, not materialized).
const factItem = "item"

// itemFact is factItem's payload: the (run, seq) idempotency key, the payload
// case name, and the delta's SIZE (chars) — never its text.
type itemFact struct {
	RunID string `json:"run_id"`
	Seq   uint64 `json:"seq"`
	Kind  string `json:"kind"`
	Chars int    `json:"chars,omitempty"`
}

// itemFlushThreshold bounds the unflushed buffer (and with it the runner's
// unacked re-emission window).
const itemFlushThreshold = 32

// itemKind names an AgentEvent's payload case — the fold's count key. Kinds
// with their OWN durability/handling (custom, summary, artifact_produced)
// are not item facts; "" marks those and the unknown/foreign payloads.
func itemKind(ev *agentcoordpb.AgentEvent) string {
	switch ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_RunStarted:
		return "run_started"
	case *agentcoordpb.AgentEvent_StepStarted:
		return "step_started"
	case *agentcoordpb.AgentEvent_StepCompleted:
		return "step_completed"
	case *agentcoordpb.AgentEvent_StatusChanged:
		return "status_changed"
	case *agentcoordpb.AgentEvent_RunCompleted:
		return "run_completed"
	case *agentcoordpb.AgentEvent_MessageStarted:
		return "message_started"
	case *agentcoordpb.AgentEvent_MessageDelta:
		return "message_delta"
	case *agentcoordpb.AgentEvent_MessageCompleted:
		return "message_completed"
	case *agentcoordpb.AgentEvent_ToolCallStarted:
		return "tool_call_started"
	case *agentcoordpb.AgentEvent_ToolCallArgsDelta:
		return "tool_call_args_delta"
	case *agentcoordpb.AgentEvent_ToolCallCompleted:
		return "tool_call_completed"
	case *agentcoordpb.AgentEvent_Interaction:
		return "interaction"
	case *agentcoordpb.AgentEvent_Raw:
		return "raw"
	default:
		return ""
	}
}

// itemIsDelta marks the storm kinds that buffer without forcing a flush.
func itemIsDelta(kind string) bool {
	return kind == "message_delta" || kind == "tool_call_args_delta"
}

// itemChars measures a delta's size — the counted-not-materialized figure.
func itemChars(ev *agentcoordpb.AgentEvent) int {
	switch p := ev.GetPayload().(type) {
	case *agentcoordpb.AgentEvent_MessageDelta:
		return len(p.MessageDelta.GetText())
	case *agentcoordpb.AgentEvent_ToolCallArgsDelta:
		return len(p.ToolCallArgsDelta.GetArgsJsonFragment())
	default:
		return 0
	}
}

// bufferItem appends one item fact to the channel's group-fsync buffer,
// flushing at a boundary kind or a full buffer.
func (c *Coordinator) bufferItem(ch *runChan, ev *agentcoordpb.AgentEvent, kind string) {
	fact := factAt(factItem, c.now(), itemFact{
		RunID: ch.id.RunID,
		Seq:   ev.GetSeq(),
		Kind:  kind,
		Chars: itemChars(ev),
	})
	c.mu.Lock()
	ch.items = append(ch.items, fact)
	full := len(ch.items) >= itemFlushThreshold
	c.mu.Unlock()
	if full || !itemIsDelta(kind) {
		c.flushItems(ch)
	}
}

// flushItems appends+fsyncs the channel's buffered item facts in ONE journal
// window (the group-fsync), then advances the cumulative Ack through the
// highest processed seq. A failed append advances nothing: the runner keeps
// the events unacked and re-emits them on reconnect (the fold dedupes).
func (c *Coordinator) flushItems(ch *runChan) {
	c.mu.Lock()
	facts := ch.items
	ch.items = nil
	target := ch.ackSeq
	c.mu.Unlock()
	if len(facts) > 0 {
		if err := c.items.Exec(func() ([]Fact, error) { return facts, nil }); err != nil {
			clidiag.Warn("ctxloom", "coordinator: journal %d item events for %s: %v (unacked; the runner re-emits)", len(facts), ch.role, err)
			return
		}
	}
	c.mu.Lock()
	if target > ch.flushedSeq {
		ch.flushedSeq = target
	}
	ack := ch.flushedSeq
	c.mu.Unlock()
	c.ackThrough(ch, ack)
}

// itemsFold COUNTS journaled items per run — the replay-equivalence
// projection for plane-1 (counts by kind + delta volume + the (run, seq)
// dedupe watermark; no delta text exists to materialize).
type itemsFold struct {
	counts map[string]map[string]int // run_id → kind → count
	chars  map[string]int            // run_id → total delta chars
	maxSeq map[string]uint64         // run_id → highest journaled seq
}

func newItemsFold() *itemsFold {
	return &itemsFold{
		counts: make(map[string]map[string]int),
		chars:  make(map[string]int),
		maxSeq: make(map[string]uint64),
	}
}

func (f *itemsFold) apply(fact Fact) {
	if fact.Kind != factItem {
		return
	}
	var p itemFact
	if fact.decode(&p) != nil {
		return
	}
	// (run, seq) idempotency: a runner reissue after reconnect re-journals
	// only what a crash lost — anything at or below the watermark is a dupe.
	if p.Seq != 0 && p.Seq <= f.maxSeq[p.RunID] {
		return
	}
	if p.Seq > f.maxSeq[p.RunID] {
		f.maxSeq[p.RunID] = p.Seq
	}
	if f.counts[p.RunID] == nil {
		f.counts[p.RunID] = make(map[string]int)
	}
	f.counts[p.RunID][p.Kind]++
	f.chars[p.RunID] += p.Chars
}

// countsFor returns a copy of the run's item counts by kind.
func (f *itemsFold) countsFor(runID string) map[string]int {
	out := make(map[string]int, len(f.counts[runID]))
	for k, v := range f.counts[runID] {
		out[k] = v
	}
	return out
}
