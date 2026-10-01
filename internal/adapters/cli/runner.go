package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/iox"
	"github.com/ctxloom/ctxloom/internal/shared/parentwatch"
	"github.com/ctxloom/ctxloom/internal/shared/version"
)

// runnerCmd is the runner process: `ctxloom runner <engine>` is what
// spawn.StartRunner starts for a host child and as a container's foreground
// process alike. It reads NO config: the reach-back trio on its environment
// says where to dial, and everything else — the label body, the package, the
// endpoint to bind, the cell — arrives on the Launch over StartRun. It
// blocks until the coordinator tears it down (RunnerHandle.Kill), a stop
// signal or its parent's exit (runnerContext), or a cancelled root context.
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
	backend := hosted.Backend(runner.RunLaunchSpec)
	ctx, stop, err := runnerContext(cmd.Context())
	if err != nil {
		return err
	}
	defer stop()
	defer divertRunnerDiagnostics(os.Getenv)()
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
			return runnerDepsFor(backend, engineName, host, interaction.Endpoint{Home: home, Reporter: App().Reporter})
		},
	})
}

// divertRunnerDiagnostics sends this runner's warnings to the file its
// originator names (sessions.EnvDiagnosticsLog) instead of stderr, which is
// the engine's pty. A file that cannot be written — a container runner handed
// a host path — leaves them on stderr: disturbing the display beats losing
// them. Returns the restore.
func divertRunnerDiagnostics(getenv func(string) string) func() {
	path := getenv(sessions.EnvDiagnosticsLog)
	if path == "" {
		return func() {}
	}
	if err := iox.WriteFileInPlace(path, iox.AppendInPlace, nil, 0o644); err != nil {
		return func() {}
	}
	return clidiag.SetSink(appendLog(path))
}

// appendLog is a warning sink that appends each warning to the file it
// names. Warnings are rare, so each opens, appends and closes.
type appendLog string

func (p appendLog) Write(b []byte) (int, error) {
	if err := iox.WriteFileInPlace(string(p), iox.AppendInPlace, b, 0o644); err != nil {
		return 0, err
	}
	return len(b), nil
}

// runnerContext is the context the runner lives under: it ends on a stop
// signal or when the process that spawned the runner exits, and either way
// the runner unwinds through its own teardown rather than dying mid-turn.
// A parent watch that cannot be armed fails the runner: running on unwatched
// is how a hard-killed host's runner outlives it.
//
// SIGHUP is a stop signal because a pty-hosted runner leads its session and
// the kernel hangs it up when the originator's master is last closed — the
// death signal darwin delivers for free. Left to its default it exits with
// no teardown; inherited as ignored (nohup) it is lost. Notify handles it
// either way.
func runnerContext(parent context.Context) (context.Context, context.CancelFunc, error) {
	ctx, stopSignals := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	ctx, stopWatch, err := parentwatch.WithParent(ctx)
	stop := func() {
		stopWatch()
		stopSignals()
	}
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("runner: watch the spawning process: %w", err)
	}
	return ctx, stop, nil
}

func init() {
	rootCmd.AddCommand(runnerCmd)
}
