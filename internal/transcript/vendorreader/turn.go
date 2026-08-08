package vendorreader

import "github.com/ctxloom/ctxloom/internal/shared/agent"

// FlushComplete records *pending as a Complete boundary ChatEvent via
// record and clears it (sets *pending to nil) ONLY once record has returned
// successfully, if and only if *pending is non-nil. A nil *pending is a
// normal, silent no-op — most vendor files end cleanly on their own
// turn-boundary signal, which already flushed and cleared it before end of
// file.
//
// The clear-after-success ordering is the contract, not an implementation
// detail: clearing first would mean a failed flush destroyed the very
// boundary it failed to write, leaving the caller nothing to retry with and
// turning any retry into a nil-pending no-op that returns success for zero
// bytes written.
//
// This is the shared shape of codex's and claude's own flushPending methods
// (each vendor's boundary-DETECTION logic — codex correlates two envelope
// types with no shared id across an unbounded run of intermediate
// token_count events before task_complete closes them; claude correlates by
// watching message.id change across content-block lines of the SAME
// response — stays in that vendor's own converter, since that part is
// genuinely different per vendor). Only the flush mechanics — "a still-open
// boundary at end of file is real, captured data, not something to silently
// drop" — are identical enough to share. record is the vendor's own
// error-wrapping Record call (see RecordFunc), not transcript.Recorder
// directly, so a flush failure still carries that vendor's own error prefix.
func FlushComplete(pending **agent.TurnMeta, record func(agent.ChatEvent) error) error {
	if *pending == nil {
		return nil
	}
	if err := record(agent.ChatEvent{Complete: *pending}); err != nil {
		return err
	}
	*pending = nil
	return nil
}
