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

// The engine events the approval hooks are spawned for. The hook command
// hands the name back to the runner, whose engine codec decodes the payload
// for that event.
const (
	approvalEventPermissionRequest = "PermissionRequest"
	approvalEventPreToolUse        = "PreToolUse"
)

// approvalPreToolMatcher names the tools a permission ask cannot answer:
// a question is answered and a plan approved only by rewriting the call's
// input, which only the pre-tool hook can do.
const approvalPreToolMatcher = "AskUserQuestion|ExitPlanMode"

// ApprovalHooks are the hooks that route what a run's posture and rules
// leave open to the human at the root: the permission ask for every such
// tool call, and the pre-tool hook for a question or a plan. Each runs
// `ctxloom hook permission`, which hands the engine's payload to the
// runner hosting the run and writes the decision back. timeout is the run's
// approval timeout; each hook outlives it by ApprovalHookSlack.
func ApprovalHooks(timeout time.Duration) wire.UnifiedHooks {
	return wire.UnifiedHooks{
		PermissionAsk: []wire.Hook{approvalHook(approvalEventPermissionRequest, "", timeout)},
		PreTool:       []wire.Hook{approvalHook(approvalEventPreToolUse, approvalPreToolMatcher, timeout)},
	}
}

func approvalHook(event, matcher string, timeout time.Duration) wire.Hook {
	return wire.Hook{
		Command: fmt.Sprintf("%s hook permission --event %s", shellSingleQuote(CtxloomCommand()), event),
		Matcher: matcher,
		Type:    "command",
		Timeout: int((timeout + ApprovalHookSlack).Seconds()),
	}
}
