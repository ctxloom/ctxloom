package spool

import (
	"fmt"
	"time"
)

// A ROUTED OUT/ MESSAGE IS DELETED; ITS IDENTITY IS WHAT SURVIVES IT.
//
// Once the coordinator has routed an out/ message to its recipient, the file is
// removed and its Entry.Identity is recorded as an empty file
// out/routed/<identity> — the in/delivered/ record's twin for the other
// direction, with the same record-then-delete order and the same retention
// (identityRecord). A sweep that finds an out/ file whose identity is already
// recorded is looking at an interrupted delete: it finishes the delete and does
// not route the message again.
//
// What tells a routed message from a refused one on disk is this record beside
// out/failed/; the routed message's text lives on in its recipient's inbox.

// routedRecord is the outbox's routed-identity record.
var routedRecord = identityRecord{rel: "out/routed", what: "routed"}

// Consume acknowledges one routed out/ message: it records identity in the
// spool's routed record, durably, and only then deletes ref's file. ref must
// address out/.
//
// A file that is already gone is ErrAlreadyGone, returned AFTER the record is
// written. Each call also prunes routed entries older than DeliveredRetention
// as of now.
func Consume(m PathMapper, ref Ref, identity string, now time.Time) error {
	if ref.Dir != DirOut {
		return fmt.Errorf("spool: %s is not an outbox entry; only %q is routed from (an inbox message is delivered with Deliver)", ref, DirOut)
	}
	return routedRecord.finish(m, ref, DirOut, identity, now)
}

// Routed reports whether identity is in harp's routed record.
func Routed(m PathMapper, harp, identity string) (bool, error) {
	return routedRecord.has(m, harp, identity)
}
