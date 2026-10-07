package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
)

// hookEngineFlagName is agent.HookEngineFlag without its dashes: the flag the
// hooks approach writes into every ctxloom callback it delivers, naming the
// engine that fires it.
var hookEngineFlagName = strings.TrimPrefix(agent.HookEngineFlag, "--")

func init() {
	hookCmd.PersistentFlags().String(hookEngineFlagName, "",
		"the engine firing this hook; written by the hooks approach that delivered it")
}

// firingEngine is the engine a hook verb was delivered to, resolved through
// the registry. Every hook verb that reads a payload or answers one needs it:
// the payload's shape is that engine's, and only its codec
// (engine.HookCodec) knows it. A verb invoked without one was not installed
// by a hooks approach — refused, never guessed.
func firingEngine(cmd *cobra.Command) (engine.Engine, error) {
	var name string
	if f := cmd.Flag(hookEngineFlagName); f != nil {
		name = f.Value.String()
	}
	if name == "" {
		return nil, fmt.Errorf("no %s: this hook was not installed by an engine's hooks approach, so nothing says whose payload it reads (re-deliver the hooks: relaunch the session, or `ctxloom manage hooks install`)", agent.HookEngineFlag)
	}
	kind, ok := App().Engines().Lookup(engine.Name(name))
	if !ok {
		return nil, fmt.Errorf("%s %q names no registered engine", agent.HookEngineFlag, name)
	}
	return kind, nil
}

// readHookEvent reads the hook payload on stdin to EOF — closing it early is
// reported by some engines as a failed hook — and decodes it through the
// firing engine's codec for event.
func readHookEvent(cmd *cobra.Command, codec engine.HookCodec, event string) (engine.HookEvent, error) {
	raw, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return engine.HookEvent{}, fmt.Errorf("read the hook payload: %w", err)
	}
	return codec.Decode(event, raw)
}

// writeHookResponse renders r as the firing engine's answer to event and
// writes it whole: encoded to bytes first, so a failure cannot leave a partial
// answer on the engine's input channel. A non-zero native exit status is
// returned as the error the process exits with.
func writeHookResponse(cmd *cobra.Command, codec engine.HookCodec, event string, r engine.HookResponse) error {
	reply, err := codec.Encode(event, r)
	if err != nil {
		return err
	}
	if _, err := cmd.OutOrStdout().Write(reply.Stdout); err != nil {
		return err
	}
	if reply.Exit != 0 {
		return &ExitError{Code: reply.Exit}
	}
	return nil
}
