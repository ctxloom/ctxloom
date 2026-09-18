package agentcoord

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fill(runID string, seq uint64) *AgentEvent {
	return &AgentEvent{RunId: runID, Seq: seq, Payload: &AgentEvent_Custom{Custom: &CustomEvent{Name: "fill"}}}
}

func marker(rs ...*EventsLost_Range) *AgentEvent {
	return &AgentEvent{Payload: &AgentEvent_EventsLost{EventsLost: &EventsLost{Lost: rs}}}
}

func rng(runID string, first, last uint64) *EventsLost_Range {
	return &EventsLost_Range{RunId: runID, FirstSeq: first, LastSeq: last}
}

// observe runs one event and returns the loss count reported (0 for none).
func observe(t *testing.T, w *SeqWatch, ev *AgentEvent) (count int, isMarker bool) {
	t.Helper()
	lost, isMarker := w.Observe(ev)
	return lost.Count(), isMarker
}

// The seq-jump disciplines every WatchRuns reader relied on before the hub
// announced its own losses: a mid-run join's first seq is the baseline, a
// reconnect reissue (seq at or below the watermark) is a duplicate, seq 0 is
// never accounted, and a jump is exactly the skipped count.
func TestSeqWatch_Observe_SeqJumpDisciplines(t *testing.T) {
	type step struct {
		seq  uint64
		want int
	}
	cases := []struct {
		name  string
		steps []step
	}{
		{"mid-run baseline join is not a loss", []step{{41, 0}, {42, 0}, {43, 0}}},
		{"contiguous", []step{{1, 0}, {2, 0}, {3, 0}}},
		{"a jump is exactly the skipped count", []step{{1, 0}, {5, 3}}},
		{"a reissued duplicate is not a loss and does not regress the watermark", []step{{1, 0}, {2, 0}, {3, 0}, {2, 0}, {4, 0}}},
		{"seq 0 is not accounted and does not disturb its neighbors", []step{{1, 0}, {0, 0}, {2, 0}}},
		{"after a jump, accounting resumes contiguously", []step{{1, 0}, {5, 3}, {6, 0}, {7, 0}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &SeqWatch{RunID: "r"}
			for _, s := range tc.steps {
				got, isMarker := observe(t, w, fill("r", s.seq))
				assert.False(t, isMarker)
				assert.Equal(t, s.want, got, "seq %d", s.seq)
			}
		})
	}
}

// The hub's marker is authoritative, and a reader that also infers loss
// from seq jumps must not count one loss twice: only the part of each range
// past the watermark is new, the watermark advances over the marker, and the
// event that follows it is then contiguous.
func TestSeqWatch_Observe_MarkerCountedOnceAgainstTheJumpThatFollows(t *testing.T) {
	w := &SeqWatch{RunID: "r"}
	observe(t, w, fill("r", 1))

	got, isMarker := observe(t, w, marker(rng("r", 2, 4)))
	assert.True(t, isMarker, "the marker carries nothing to render")
	assert.Equal(t, 3, got, "the marker's exact count: 2, 3, 4")

	got, _ = observe(t, w, fill("r", 5))
	assert.Equal(t, 0, got, "seq 5 follows the marker contiguously; the jump it would have revealed was the marker's")
}

func TestSeqWatch_Observe_MarkerRangesAreReconciledWithTheWatermark(t *testing.T) {
	cases := []struct {
		name    string
		before  []uint64 // seqs observed before the marker
		ranges  []*EventsLost_Range
		want    int
		wantTxt string
	}{
		{"whole range past the watermark", []uint64{4}, []*EventsLost_Range{rng("r", 5, 8)}, 4, "5..8"},
		{"range entirely below the watermark (an eviction the jump already revealed)", []uint64{1, 10}, []*EventsLost_Range{rng("r", 2, 3)}, 0, ""},
		{"range straddling the watermark counts only the tail", []uint64{4}, []*EventsLost_Range{rng("r", 3, 6)}, 2, "5..6"},
		{"no watermark yet: a loss before the first delivered event is still a loss", nil, []*EventsLost_Range{rng("r", 1, 3)}, 3, "1..3"},
		{"a lower range listed after a higher one is still new (the hub records refused events before evicted ones)", []uint64{10}, []*EventsLost_Range{rng("r", 300, 301), rng("r", 11, 12)}, 4, "11..12, 300..301"},
		{"another run's ranges are not this reader's loss", []uint64{1}, []*EventsLost_Range{rng("other", 2, 99), rng("r", 2, 2)}, 1, "2..2"},
		{"empty marker", []uint64{7}, nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := &SeqWatch{RunID: "r"}
			for _, s := range tc.before {
				observe(t, w, fill("r", s))
			}
			lost, isMarker := w.Observe(marker(tc.ranges...))
			require.True(t, isMarker)
			assert.Equal(t, tc.want, lost.Count())
			assert.Equal(t, tc.wantTxt, lost.RangesText())
		})
	}
}

// A stream that is already scoped to one run by its subscription
// (ConsumerService.WatchRuns with run_ids) carries events that need no
// run_id check — and hermetic fixtures on that path stamp none. An unscoped
// watch (RunID empty) accounts every event and every marker range as the
// run's own.
func TestSeqWatch_Observe_UnscopedWatchAccountsEveryEvent(t *testing.T) {
	w := &SeqWatch{}
	got, _ := observe(t, w, fill("", 1))
	assert.Equal(t, 0, got)
	got, _ = observe(t, w, fill("", 4))
	assert.Equal(t, 2, got, "the jump from 1 to 4 is two lost events even with no run_id on the wire")
	lost, isMarker := w.Observe(marker(rng("r", 5, 6)))
	assert.True(t, isMarker)
	assert.Equal(t, 2, lost.Count(), "an unscoped watch keeps every range the marker names")
}

// A hub-wide subscription carries other runs' events until it is narrowed
// (cli.renderOwnedRunEvents subscribes before its run's ID exists); they are
// neither this run's baseline nor its jump.
func TestSeqWatch_Observe_OtherRunsEventsAreIgnored(t *testing.T) {
	w := &SeqWatch{RunID: "r"}
	got, _ := observe(t, w, fill("other", 40))
	assert.Equal(t, 0, got)
	got, _ = observe(t, w, fill("r", 1))
	assert.Equal(t, 0, got, "r's first seq is r's baseline, whatever the other run was at")
	got, _ = observe(t, w, fill("other", 90))
	assert.Equal(t, 0, got)
	got, _ = observe(t, w, fill("r", 2))
	assert.Equal(t, 0, got, "the other run's seqs never moved r's watermark")
}
