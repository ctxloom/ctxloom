package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	runnermcp "github.com/ctxloom/ctxloom/internal/adapters/runner/mcp"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// runnerCmd is the runner process: `ctxloom runner <engine>` is what
// spawn.StartRunner starts for a host child and as a container's foreground
// process alike. It reads NO config: the reach-back trio on its environment
// says where to dial, and everything else — the label body, the package, the
// endpoint to bind, the cell — arrives on the Launch over StartRun. It
// blocks until the coordinator tears it down (RunnerHandle.Kill), a
// SIGINT/SIGTERM, or a cancelled root context.
var runnerCmd = &cobra.Command{
	Use:    "runner <engine>",
	Short:  "Run as the engine runner for one launch (internal use)",
	Long:   `Starts the ctxloom binary as the runner that hosts one launch of the named engine. Started by ctxloom itself, on the host or as a container's foreground process; the launch arrives from the coordinator that spawned it.`,
	Args:   cobra.ExactArgs(1),
	Hidden: true,
	RunE:   runRunner,
}

func runRunner(cmd *cobra.Command, args []string) error {
	engineName := args[0]
	hosted, ok := engines.Hosted(engineName)
	if !ok {
		return fmt.Errorf("unknown engine: %s", engineName)
	}
	backend := hosted.Backend(backends.RunLaunchSpec)
	ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return runner.Main(ctx, runner.MainDeps{
		Reporter: App().Reporter,
		Harness:  engineName,
		Version:  version.Version,
		Getenv:   os.Getenv,
		Unsetenv: os.Unsetenv,
		Ports: func(host *runner.EngineHost, home *runner.Home) (runner.Deps, error) {
			// The engine's own input-state gate, discovered like every other
			// optional capability; a backend without one makes the injector
			// REFUSE to inject rather than assume the terminal is safe to
			// write into (runner.TerminalInjector).
			gate, _ := backend.(agent.InputGate)
			host.BindTerminal(stdioTerminal{
				backend:  backend,
				injector: runner.NewTerminalInjector(home, gate),
				stdin:    os.Stdin,
				stdout:   os.Stdout,
				stderr:   os.Stderr,
			})
			return runnerDepsFor(backend, engineName, host, runnermcp.Endpoint{Home: home, Reporter: App().Reporter})
		},
	})
}

func init() {
	rootCmd.AddCommand(runnerCmd)
}
