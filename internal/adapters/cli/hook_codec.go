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
// (engine.HookCodec) knows it. A delivered hook names it explicitly
// (agent.BindHooks writes --engine); an entry without one — written by an
// earlier ctxloom, or by hand — is the registry's default engine
// (Registry.Default, the same setup-level default materialize uses). An
// explicit --engine naming no registered engine is refused
// (UnknownHookEngineError), never rounded to the default.
func firingEngine(cmd *cobra.Command) (engine.Engine, error) {
	var name string
	if f := cmd.Flag(hookEngineFlagName); f != nil {
		name = f.Value.String()
	}
	reg := App().Engines()
	if name == "" {
		return reg.Default()
	}
	kind, ok := reg.Lookup(engine.Name(name))
	if !ok {
		return nil, &UnknownHookEngineError{Name: name}
	}
	return kind, nil
}

// UnknownHookEngineError refuses a hook verb whose --engine names no
// registered engine: whose payload it reads is unknown, and guessing would
// decode one engine's wire with another's codec.
type UnknownHookEngineError struct{ Name string }

func (e *UnknownHookEngineError) Error() string {
	return fmt.Sprintf("%s %q names no registered engine", agent.HookEngineFlag, e.Name)
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
