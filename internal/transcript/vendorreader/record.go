package vendorreader

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// RecordFunc returns a closure that calls rec.Record and, on failure, wraps
// the error with vendor's own prefix ("codex: record: ...", "claude:
// record: ...", etc.) — every adapter's converter needs exactly this, and
// every one of them built its own identically-shaped method to get it. The
// returned func is what a converter stores and calls repeatedly instead of
// holding the Recorder directly, so callers like FlushComplete never need to
// know which vendor they're flushing for.
func RecordFunc(rec transcript.Recorder, vendor string) func(agent.ChatEvent) error {
	return func(ev agent.ChatEvent) error {
		if err := rec.Record(ev); err != nil {
			return fmt.Errorf("%s: record: %w", vendor, err)
		}
		return nil
	}
}
