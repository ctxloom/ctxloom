package agentcoord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func lostRanges(rs ...[2]uint64) *EventsLost {
	m := &EventsLost{}
	for _, r := range rs {
		m.Lost = append(m.Lost, &EventsLost_Range{RunId: "r", FirstSeq: r[0], LastSeq: r[1]})
	}
	return m
}

// A reader that already infers loss from seq jumps must not count the same
// loss twice when the hub's marker names it: only the part of each range past
// the reader's watermark is new, and the watermark moves to the marker's end.
func TestEventsLost_NewPast_CountsOnlyWhatTheWatermarkHasNotSeen(t *testing.T) {
	cases := []struct {
		name     string
		m        *EventsLost
		lastSeq  uint64
		wantGap  int
		wantLast uint64
	}{
		{"whole range past the watermark", lostRanges([2]uint64{5, 8}), 4, 4, 8},
		{"range entirely below the watermark (an eviction the seq jump already revealed)", lostRanges([2]uint64{2, 3}), 10, 0, 10},
		{"range straddling the watermark counts only the tail", lostRanges([2]uint64{3, 6}), 4, 2, 6},
		{"no watermark yet: a loss before the first delivered event is still a loss", lostRanges([2]uint64{1, 3}), 0, 3, 3},
		{"several ranges, mixed", lostRanges([2]uint64{20, 21}, [2]uint64{2, 3}), 10, 2, 21},
		{"a lower range listed after a higher one is still new (the hub records drops before evictions)", lostRanges([2]uint64{300, 301}, [2]uint64{11, 12}), 10, 4, 301},
		{"empty marker", &EventsLost{}, 7, 0, 7},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gap, last := tc.m.NewPast(tc.lastSeq)
			assert.Equal(t, tc.wantGap, gap, "gap")
			assert.Equal(t, tc.wantLast, last, "advanced watermark")
		})
	}
}
