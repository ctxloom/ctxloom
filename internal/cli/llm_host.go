package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
)

// llmHostCmd is the docker-direct runner mode: `ctxloom llm host <backend>`
// is `llm serve` MINUS plugin.Serve. It stands the runner up (EngineHost +
// dial-home + runner-local MCP, all via the shared standUpRunner), hosts the
// delegated StructuredChat run IN-PROCESS over Transport 2 (StartRun on its
// RunnerChannel), and then BLOCKS until torn down — it opens NO go-plugin
// listener, so a container running this command publishes no port and
// exposes no runner socket (closing the peer-container hole a listening
// port would open). Teardown is external: the coordinator's
// isolation.RunnerHandle.Kill (`docker rm -f` for a container, a setsid session
// sweep for a host runner). A SIGINT/SIGTERM or a cancelled root context also
// unblocks it so a bare foreground invocation can be stopped.
var llmHostCmd = &cobra.Command{
	Use:    "host <backend>",
	Short:  "Run as an in-process engine host (internal use, no plugin transport)",
	Long:   `Starts the ctxloom binary as an engine-runner host for the specified backend, hosting the engine in-process over Transport 2 with NO go-plugin listener. Used internally by the delegated StartRun spawn path.`,
	Args:   cobra.ExactArgs(1),
	Hidden: true,
	RunE:   runLLMHost,
}

func runLLMHost(cmd *cobra.Command, args []string) error {
	// Fail-loudly gate: same shape as `llm serve` — see its
	// RunE for the full rationale.
	gates := newPhaseGates(os.Stderr)

	backendName := args[0]

	backend := backends.Get(backendName)
	if backend == nil {
		return fmt.Errorf("unknown backend: %s", backendName)
	}

	standup, err := standUpRunner(cmd, backend, backendName, llmHostLabel)
	if err != nil {
		return err
	}
	if ferr := gates.close(PhaseStartup); ferr != nil {
		return ferr
	}

	// The runner stays warm across turns; the coordinator tears it down
	// (RunnerHandle.Kill) when the child ends or at a one-shot turn
	// boundary. Block until that (or a signal / cancelled context) — there
	// is no plugin.Serve to hold the process here.
	waitForRunnerTermination(cmd)

	standup.teardown()
	return nil
}

// waitForRunnerTermination blocks the `llm host` runner until its root context
// is cancelled or it receives SIGINT/SIGTERM. A container teardown (`docker rm
// -f`) delivers SIGKILL, which ends the process regardless; this handles the
// graceful/foreground cases.
func waitForRunnerTermination(cmd *cobra.Command) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	select {
	case <-cmd.Context().Done():
	case <-sigCh:
	}
}

// llmHostLabel is host's own --label, the counterpart to `llm serve`'s
// llmServeLabel; RunE hands it to standUpRunner as that call's label.
var llmHostLabel string

func init() {
	llmCmd.AddCommand(llmHostCmd)
	llmHostCmd.Flags().StringVar(&llmHostLabel, "label", "", "Config label to apply (internal; passed by the StartRun spawn path)")
}
