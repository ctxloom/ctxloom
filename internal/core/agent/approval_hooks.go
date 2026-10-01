package agent

import (
	"fmt"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// ApprovalHookSlack is how much longer an approval hook may run than the
// approval timeout it waits out: the coordinator's deny at the deadline must
// still reach the engine through the hook, not be cut off by the engine
// killing the hook first.
const ApprovalHookSlack = 60 * time.Second

// ApprovalHook is one hook of an engine's approval route: it runs `ctxloom
// hook permission`, which hands the engine's payload to the runner hosting
// the run and writes the decision back. event is the engine's own name for
// the hook event, handed back to the engine's codec to decode the payload;
// matcher is the engine's own matcher ("" for every tool). The engine
// decides both (engine.ApprovalCodec.Hooks); the hook outlives the run's
// approval timeout by ApprovalHookSlack.
func ApprovalHook(event, matcher string, timeout time.Duration) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook permission --event %s", shellSingleQuote(CtxloomCommand()), event),
		Matcher: matcher,
		Type:    "command",
		Timeout: int((timeout + ApprovalHookSlack).Seconds()),
	}
}
