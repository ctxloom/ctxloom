package spool

import (
	"fmt"
	"time"
)

// A DELIVERED MESSAGE IS DELETED; ITS IDENTITY IS WHAT SURVIVES IT.
//
// Once a reader has delivered an in/ message (the engine accepted the turn, or
// the owner's turn-start hook wrote it out), the file is removed and its
// Entry.Identity is recorded as an empty file in/delivered/<identity>. That
// record is the inbox's whole memory of what it has delivered:
//
//   - Claim and the runner's sweep refuse a copy whose identity is recorded;
//   - a steer withdrawal answers "already delivered" from it;
//   - the coordinator credits delivery progress from it.
//
// The record's shape, its record-then-delete order and its prune are
// identityRecord's (record.go), shared with the routed record out/ keeps.

// DeliveredRetention bounds every identity record — in/delivered/ and
// out/routed/ alike: an entry older than this is pruned.
//
// It is sized to the redelivery horizon, the longest a copy of a recorded
// identity can still reach a reader. The copies that can arrive are a file
// whose delete was interrupted after its record was written, and — for
// in/delivered/ — a coordinator re-route of an out/ message it routed but had
// not yet recorded, which carries the out/ message's identity. The reader sees
// such a copy at its NEXT start: a child's next incarnation, the owner's next
// turn (which can be a session resumed days later), the coordinator's next
// sweep. A week covers that with margin; past it, the cost is one repeated
// delivery, which at-least-once already admits.
const DeliveredRetention = 7 * 24 * time.Hour

// deliveredRecord is the inbox's delivered-identity record.
var deliveredRecord = identityRecord{rel: "in/delivered", what: "delivered"}

// Deliver acknowledges one delivered inbox message: it records identity in the
// spool's delivered record, durably, and only then deletes ref's file. ref must
// address in/ or in/claimed/ — the only places a reader delivers from.
//
// A file that is already gone is ErrAlreadyGone, returned AFTER the record is
// written: whoever removed it, the identity is delivered. Each call also prunes
// record entries older than DeliveredRetention as of now.
func Deliver(m PathMapper, ref Ref, identity string, now time.Time) error {
	if ref.Dir != DirIn && ref.Dir != ClaimedDirName {
		return fmt.Errorf("spool: %s is not an inbox entry; only %q and %q are delivered from", ref, DirIn, ClaimedDirName)
	}
	return deliveredRecord.finish(m, ref, ref.Dir, identity, now)
}

// Delivered reports whether identity is in harp's delivered record.
func Delivered(m PathMapper, harp, identity string) (bool, error) {
	return deliveredRecord.has(m, harp, identity)
}

// DeliveredIdentities lists harp's delivered record: each identity with the
// time it was recorded. A spool that never delivered anything has an empty one.
func DeliveredIdentities(m PathMapper, harp string) (map[string]time.Time, error) {
	return deliveredRecord.identities(m, harp)
}
