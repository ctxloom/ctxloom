package agentcoord

import (
	"cmp"
	"slices"
)

// NewPast reports how many of the seqs this marker names lie past lastSeq, a
// reader's highest delivered seq for the run (0 when it has seen nothing yet),
// and the watermark advanced over the marker.
//
// Why a reader needs this rather than a plain count: both WatchRuns readers
// already infer loss from a jump in the wire's per-run seq, and the hub's
// marker can name seqs BELOW that watermark — an event evicted to make room
// for a terminal sat behind events the reader has since received, and the
// jump they revealed was already reported. Counting only what is new keeps
// the marker authoritative without reporting one loss twice.
//
// Ranges are walked in seq order, not wire order: the hub records the events
// a full ring refused (newest) before the ones it evicted (oldest), so a
// lower range can follow a higher one, and both can be new.
func (m *EventsLost) NewPast(lastSeq uint64) (gap int, advanced uint64) {
	ranges := slices.Clone(m.GetLost())
	slices.SortFunc(ranges, func(a, b *EventsLost_Range) int {
		return cmp.Compare(a.GetFirstSeq(), b.GetFirstSeq())
	})
	advanced = lastSeq
	for _, r := range ranges {
		first, last := r.GetFirstSeq(), r.GetLastSeq()
		if last <= advanced {
			continue
		}
		if first <= advanced {
			first = advanced + 1
		}
		gap += int(last - first + 1)
		advanced = last
	}
	return gap, advanced
}
