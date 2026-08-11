package vendorreader

import (
	"context"
	"fmt"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// ConvertJSONLLines runs the "two-pass adapter" shape codex.go's package doc
// names as the pattern every JSONL-per-session engine's Convert copies: emit
// a single up-front Session event (if info is non-nil — a vendor transcript
// with no session metadata anywhere emits none, per SessionInfoBuilder's
// contract), then stream every line in order, checking ctx cancellation
// before each one and handing the line to dispatch to decode and route it,
// then call flush once every line has been processed (a vendor with no
// pending-boundary merge to do simply passes a flush that does nothing).
//
// dispatch and flush carry everything genuinely vendor-specific — the
// envelope shape a line decodes into and which handler each discriminator
// value routes to, and (for codex/claude) the pending-Complete
// boundary-detection logic FlushComplete's caller owns. What's left here is
// pure boilerplate: not something a reader benefits from seeing reimplemented
// per vendor, and exactly what reprise's duplicate gate started flagging
// once codex's and claude's copies converged on calling the same helpers for
// everything else.
func ConvertJSONLLines(ctx context.Context, rec transcript.Recorder, lines [][]byte, vendor string, info *agent.ChatSessionInfo, dispatch func(line []byte) error, flush func() error) error {
	if info != nil {
		if err := rec.Record(agent.ChatEvent{Session: info}); err != nil {
			return fmt.Errorf("%s: record session: %w", vendor, err)
		}
	}

	for _, line := range lines {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := dispatch(line); err != nil {
			return err
		}
	}
	return flush()
}
