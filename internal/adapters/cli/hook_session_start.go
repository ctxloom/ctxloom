package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// hookSessionStartCmd is ctxloom's own SessionStart callback. It NEVER
// delivers the project's assembled context: that is the session's system
// prompt (claude's context approach), and a second copy here is the
// duplication this hook was cut down to remove. What it delivers is what
// only a session-start moment can: the user-facing notices (the agent-setup
// nudge). Engines own compaction, so it carries no recovered context.
var hookSessionStartCmd = &cobra.Command{
	Use:    "session-start",
	Hidden: true, // Machine callback (SessionStart hook) - not for direct use
	Short:  "Deliver session-start notices to an AI tool's SessionStart hook",
	Long: `Reads the session_start payload on stdin, through the codec of the engine
--engine names, and answers through the same codec with the session-start
notices for the user. It never carries the project's context, which the
session already has.`,
	Args:          cobra.NoArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          runHookSessionStart,
}

// runHookSessionStart answers one session_start. A payload it cannot read is
// an error and the process exits non-zero: an engine shows a failed hook,
// which is the truth, where an answer built from an empty payload would claim
// a startup that never happened. A panic exits non-zero for the same reason.
func runHookSessionStart(cmd *cobra.Command, _ []string) (err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "ctxloom hook session-start: panic: %v\n", r)
			err = fmt.Errorf("session-start hook panicked: %v", r)
		}
	}()
	kind, err := firingEngine(cmd)
	if err != nil {
		return err
	}
	codec := kind.Hooks()
	ev, err := readHookEvent(cmd, codec, wire.HookEventSessionStart)
	if err != nil {
		return err
	}
	return writeHookResponse(cmd, codec, wire.HookEventSessionStart, sessionStartResponse(ev))
}

// sessionStartResponse is what one session_start earns: the user-facing
// notices. The payload is read (and must parse) so a malformed one fails the
// hook, but nothing in it changes the answer today.
func sessionStartResponse(engine.HookEvent) engine.HookResponse {
	return engine.HookResponse{Notice: agentSetupNudge()}
}

// agentSetupNudge returns the "profiles but no agents" nudge for the project
// this hook process runs in, or "" when it should not fire. It reads this
// process's own generation and delegates the trigger decision to
// operations.AgentSetupNudge; a config refusal yields "" and never blocks
// startup.
func agentSetupNudge() string {
	cfg, err := GetConfig()
	if err != nil {
		return ""
	}
	return operations.AgentSetupNudge(cfg)
}

func init() {
	hookCmd.AddCommand(hookSessionStartCmd)
}
