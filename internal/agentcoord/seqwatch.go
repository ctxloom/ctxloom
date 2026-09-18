package agentcoord

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// SeqWatch is a WatchRuns reader's per-run loss accounting. The hub's ring
// is bounded and can lose a subscriber's events; two signals say so, and a
// reader that heeds both must not report one loss twice:
//
//   - the hub's own synthetic EventsLost marker (coord/consumer.go) — exact
//     per-run seq ranges, delivered ahead of the next event that fit;
//   - a jump in the wire's per-run seq (monotonic, starts at 1, no gaps at
//     the source — coordination.proto's AgentEvent.seq contract), which
//     still catches a loss the hub could not announce in time (a terminal
//     that had room for itself but not for its marker).
//
// RunID scopes the accounting to one run when the SUBSCRIPTION is not
// already scoped: a hub-wide subscription (cli.renderOwnedRunEvents
// subscribes before its run's ID exists) carries other runs' events and
// other runs' marker ranges, which are not this reader's baseline, jump, or
// loss. Leave it empty for a stream the subscription already confines to one
// run (ConsumerService.WatchRuns with run_ids): every event and every range
// on it is the run's own, whatever run_id — if any — the wire stamped.
type SeqWatch struct {
	RunID string
	last  uint64 // highest seq delivered or announced lost; 0 until the first
}

// Observe folds one stream event into the watermark. lost names RunID's
// events lost ahead of ev (Count() 0 when none); marker reports that ev is
// the hub's EventsLost marker, which carries nothing to render.
//
// Ordinary events: the first seq observed is the baseline (a mid-run join is
// not a loss); a seq at or below the watermark is a reconnect reissue
// (home.go), a duplicate, not a loss, and does not regress the watermark;
// seq 0 is never accounted (no in-repo producer emits it; the marker itself
// has none). A marker's ranges count only past the watermark — an eviction's
// range lies BELOW events already delivered, and the jump they revealed was
// already reported.
func (w *SeqWatch) Observe(ev *AgentEvent) (lost *EventsLost, marker bool) {
	if m, ok := ev.GetPayload().(*AgentEvent_EventsLost); ok {
		lost, w.last = m.EventsLost.past(w, w.last)
		return lost, true
	}
	lost = &EventsLost{}
	seq := ev.GetSeq()
	if !w.owns(ev.GetRunId()) || seq == 0 || seq <= w.last {
		return lost, false
	}
	if w.last != 0 && seq > w.last+1 {
		lost.Lost = []*EventsLost_Range{{RunId: w.RunID, FirstSeq: w.last + 1, LastSeq: seq - 1}}
	}
	w.last = seq
	return lost, false
}

// owns reports whether an event or range stamped runID is this watch's to
// account: everything on an unscoped watch, only RunID's on a scoped one.
func (w *SeqWatch) owns(runID string) bool { return w.RunID == "" || runID == w.RunID }

// past returns the watch's ranges trimmed to what lies past last, in seq
// order, and the watermark advanced over them. Ranges are walked in seq
// order, not wire order: the hub records the events a full ring refused
// (newest) before the ones it evicted (oldest), so a lower range can follow
// a higher one, and both can be new.
func (m *EventsLost) past(w *SeqWatch, last uint64) (*EventsLost, uint64) {
	ranges := slices.Clone(m.GetLost())
	slices.SortFunc(ranges, func(a, b *EventsLost_Range) int {
		return cmp.Compare(a.GetFirstSeq(), b.GetFirstSeq())
	})
	kept := &EventsLost{}
	for _, r := range ranges {
		first, lastSeq := r.GetFirstSeq(), r.GetLastSeq()
		if !w.owns(r.GetRunId()) || lastSeq <= last {
			continue
		}
		if first <= last {
			first = last + 1
		}
		kept.Lost = append(kept.Lost, &EventsLost_Range{RunId: r.GetRunId(), FirstSeq: first, LastSeq: lastSeq})
		last = lastSeq
	}
	return kept, last
}

// Count is the number of seqs the ranges name.
func (m *EventsLost) Count() int {
	n := 0
	for _, r := range m.GetLost() {
		n += int(r.GetLastSeq() - r.GetFirstSeq() + 1)
	}
	return n
}

// RangesText renders the ranges as "2..4, 9..9" for a diagnostic.
func (m *EventsLost) RangesText() string {
	parts := make([]string, 0, len(m.GetLost()))
	for _, r := range m.GetLost() {
		parts = append(parts, fmt.Sprintf("%d..%d", r.GetFirstSeq(), r.GetLastSeq()))
	}
	return strings.Join(parts, ", ")
}
