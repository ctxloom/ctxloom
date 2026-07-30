package coord

import (
	"google.golang.org/protobuf/reflect/protoreflect"

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

// itemPayloadKinds is the set of AgentEvent payload oneof fields that ARE item
// facts, named by their proto field name — which is also the kind string the
// journal records, so the vocabulary cannot drift from the schema. An
// ALLOWLIST, deliberately: the payloads left out are the ones with their own
// durability/handling (custom, summary, artifact_produced), and a payload case
// added to the proto later must be decided about rather than silently start
// being journaled as an item.
var itemPayloadKinds = map[protoreflect.Name]bool{
	"run_started":          true,
	"step_started":         true,
	"step_completed":       true,
	"status_changed":       true,
	"run_completed":        true,
	"message_started":      true,
	"message_delta":        true,
	"message_completed":    true,
	"tool_call_started":    true,
	"tool_call_args_delta": true,
	"tool_call_completed":  true,
	"interaction":          true,
	"raw":                  true,
}

// agentEventPayload is AgentEvent's payload oneof descriptor, resolved once.
var agentEventPayload = (&agentcoordpb.AgentEvent{}).ProtoReflect().Descriptor().Oneofs().ByName("payload")

// itemKind names an AgentEvent's payload case — the fold's count key. "" marks
// the payloads that are not item facts and the unknown/foreign ones.
func itemKind(ev *agentcoordpb.AgentEvent) string {
	if ev.GetPayload() == nil {
		return ""
	}
	set := ev.ProtoReflect().WhichOneof(agentEventPayload)
	if set == nil || !itemPayloadKinds[set.Name()] {
		return ""
	}
	return string(set.Name())
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
			// U022-F20: put the facts BACK instead of dropping them — the
			// old behavior both lost them permanently (they existed in no
			// buffer, no journal, nowhere) AND let a LATER successful flush
			// falsely certify durability for them: that flush's own
			// `target := ch.ackSeq` keeps climbing independent of journal
			// success (handleAgentEvent advances it on every event), so
			// without restoring these facts, flushedSeq/ackThrough would
			// jump straight past the seqs this failed write never wrote.
			c.mu.Lock()
			ch.items = append(facts, ch.items...)
			c.mu.Unlock()
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
//
// test-only: no production caller (U022-F21) — a defensive-copy assertion
// accessor used across the dedupe/replay/offset-view/checkpoint test suites.
// Keep: returning a copy (rather than inlining a direct map read at each of
// the 10 call sites) is what makes those reads race-safe outside a View
// window.
func (f *itemsFold) countsFor(runID string) map[string]int {
	out := make(map[string]int, len(f.counts[runID]))
	for k, v := range f.counts[runID] {
		out[k] = v
	}
	return out
}

// itemsSnapshot is itemsFold's persisted state (D4 CHECKPOINT compaction):
// counts/chars/maxSeq as of Offset, a journal byte position — so a later
// startup can restore this state and replay only the TAIL of items.jsonl
// (openStoreFromOffset, journal.go) instead of re-parsing from byte 0. Pure
// replay shortcut: journal truncation stays deferred (Wave E's retention
// decision), so a missing/stale/corrupt snapshot only costs a slower full
// replay, never correctness — restore + tail-replay must equal a full
// replay from 0 (proven by TestItemsSnapshot_ReplayEquivalence).
type itemsSnapshot struct {
	Offset int64                     `json:"offset"`
	Counts map[string]map[string]int `json:"counts"`
	Chars  map[string]int            `json:"chars"`
	MaxSeq map[string]uint64         `json:"max_seq"`
}

// snapshot deep-copies the fold's current state, tagged with the journal
// offset it covers through. Caller holds the journal's Exec/View window (the
// same discipline every other fold read follows).
func (f *itemsFold) snapshot(offset int64) itemsSnapshot {
	counts := make(map[string]map[string]int, len(f.counts))
	for runID, byKind := range f.counts {
		cp := make(map[string]int, len(byKind))
		for k, v := range byKind {
			cp[k] = v
		}
		counts[runID] = cp
	}
	chars := make(map[string]int, len(f.chars))
	for k, v := range f.chars {
		chars[k] = v
	}
	maxSeq := make(map[string]uint64, len(f.maxSeq))
	for k, v := range f.maxSeq {
		maxSeq[k] = v
	}
	return itemsSnapshot{Offset: offset, Counts: counts, Chars: chars, MaxSeq: maxSeq}
}

// restore seeds the fold from a previously taken snapshot — valid only on a
// fresh (empty) fold, before any replay/apply.
func (f *itemsFold) restore(snap itemsSnapshot) {
	if snap.Counts != nil {
		f.counts = snap.Counts
	}
	if snap.Chars != nil {
		f.chars = snap.Chars
	}
	if snap.MaxSeq != nil {
		f.maxSeq = snap.MaxSeq
	}
}
