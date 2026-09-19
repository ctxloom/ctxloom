package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/shared/shellenv"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/tmuxhost"
)

// tmux is a HARD DEPENDENCY of an interactive agent run, and there is exactly
// one interactive path: the engine runs in a tmux pane. There is deliberately
// no fallback to ptyrunner when tmux is missing.
//
// A fallback is what makes a dependency untrue. If a missing tmux quietly
// produced a pty-backed run instead, the engine would still start but nothing
// could ever attach to it, so `ctxloom attach` would fail later against a run
// that looked healthy -- and the user would learn about tmux at the moment
// they needed the pane, not at the moment they could still install it. The
// refusal below moves that discovery to launch, where a remedy is actionable.
//
// This is a BREAKING UPGRADE for a user on a host without tmux: their next
// interactive run stops, loudly, naming the fix. That is the stated cost.
//
// tmuxhost itself must not grow a fallback either -- its ExecRunner doc says
// so, and that prohibition is load-bearing rather than aspirational now: the
// only alternatives available to that layer are a silent decline and a
// fabricated success.
const tmuxInstallRemedy = "install tmux and re-run: `apt install tmux` (Debian/Ubuntu), `dnf install tmux` (Fedora/RHEL), `brew install tmux` (macOS), `pacman -S tmux` (Arch)"

// resolveTmux reports whether the tmux binary can be found, resolving through
// the user's login-shell PATH for the same GUI-launch reason resolveBinaryPath
// documents. Overridable so both arms of the refusal are testable: tmux is not
// installed in this project's test environment, so the present arm would
// otherwise be unreachable and would rot silently.
var resolveTmux = func() (string, error) { return shellenv.Resolve("tmux") }

// newPaneRunner builds the tmux runner a pane-hosted launch drives. Overridable
// for the same reason as resolveTmux.
var newPaneRunner = func() tmuxhost.Runner { return tmuxhost.ExecRunner{} }

// paneViewer is the launcher's own attached viewer: it relays the pane's bytes
// to the caller's stdout and unblocks the launch when the hosted command exits.
//
// Output and Closed are called from the pane's goroutine and must not block --
// a slow viewer stalls the fanout for every other viewer of the same pane.
type paneViewer struct {
	out  io.Writer
	done chan struct{}

	mu   sync.Mutex
	code int32
	once sync.Once
}

func (v *paneViewer) Output(p []byte) { _, _ = v.out.Write(p) }

func (v *paneViewer) Closed(exitCode int32, _ string) {
	v.once.Do(func() {
		v.mu.Lock()
		v.code = exitCode
		v.mu.Unlock()
		close(v.done)
	})
}

func (v *paneViewer) exitCode() int32 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.code
}

// runInteractiveInPane hosts spec's process in a tmux pane and wires the
// caller's terminal to it, returning the hosted command's exit code.
func runInteractiveInPane(ctx context.Context, spec agent.LaunchSpec, stdin io.Reader, stdout io.Writer, resize <-chan agent.WindowSize) (int32, error) {
	if _, err := resolveTmux(); err != nil {
		// Report the finding and let strictness decide fatality -- this site
		// does not branch on degraded state, per the project's posture. The
		// error return is what guarantees NOTHING LAUNCHES either way: under
		// --degraded the choke will not abort, and the launch still stops here
		// rather than proceeding into a tmux that is not there.
		strictness.FailOnce(strictness.ClassConfig, tmuxInstallRemedy,
			"an interactive agent runs in a tmux pane, and tmux was not found: %v", err)
		return 1, fmt.Errorf("interactive launch requires tmux: %w", err)
	}

	// A pane is addressed by harp, so an unnamed run cannot be hosted in one.
	// This is reachable in production and is a SECOND breaking consequence of
	// the single pane path: run.go warns and proceeds "unharped" when
	// AssignSession fails, which used to still give the user a working
	// pty-backed engine and now cannot. Answered here, with the actual cause
	// named, rather than left to surface as tmuxhost's internal "a pane must
	// name the run it belongs to" -- which is true but tells the user nothing
	// about session naming having failed upstream.
	if spec.Harp == "" {
		return 1, fmt.Errorf("interactive launch requires a named session: this run has no harp " +
			"(session naming failed earlier), and an interactive engine is hosted in a pane addressed by harp")
	}

	tmpDir, err := os.MkdirTemp("", "ctxloom-pane-")
	if err != nil {
		return 1, fmt.Errorf("pane launch: make capture dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	host := tmuxhost.NewPaneHost(newPaneRunner(), tmpDir)

	// The full merged environment is passed explicitly rather than inherited.
	// A tmux window inherits the SERVER's environment, and that server is
	// long-lived and shared across runs -- so an inherited env would be some
	// earlier run's, not this one's.
	if err := host.Start(ctx, spec.Harp, tmuxhost.PaneSpec{
		Command: resolveBinaryPath(spec.BinaryPath),
		Args:    spec.Args,
		Cwd:     spec.WorkDir,
		Env:     envMap(spec.Env),
		Engine:  spec.Engine,
		Surface: spec.Surface,
	}); err != nil {
		return 1, fmt.Errorf("pane launch: %w", err)
	}
	// WithoutCancel: the pane must be torn down even when ctx is already
	// cancelled, which is exactly the case where the launch is unwinding.
	defer func() { _ = host.Stop(context.WithoutCancel(ctx), spec.Harp) }()

	v := &paneViewer{out: stdout, done: make(chan struct{})}
	detach, err := host.Attach(spec.Harp, v)
	if err != nil {
		return 1, fmt.Errorf("pane launch: attach: %w", err)
	}
	defer detach()

	pumpCtx, stopPumps := context.WithCancel(ctx)
	defer stopPumps()
	if stdin != nil {
		if spec.StdinCleanup != nil {
			defer spec.StdinCleanup()
		}
		go pumpStdin(pumpCtx, host, spec.Harp, stdin)
	}
	if resize != nil {
		go pumpResize(pumpCtx, host, spec.Harp, resize)
	}

	select {
	case <-v.done:
		return v.exitCode(), nil
	case <-ctx.Done():
		return 1, ctx.Err()
	}
}

// pumpStdin types the viewer's keystrokes into the pane.
func pumpStdin(ctx context.Context, host *tmuxhost.PaneHost, harp string, stdin io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := stdin.Read(buf)
		if n > 0 {
			if ierr := host.Input(ctx, harp, buf[:n]); ierr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}

// pumpResize applies the viewer's window size to the pane.
func pumpResize(ctx context.Context, host *tmuxhost.PaneHost, harp string, resize <-chan agent.WindowSize) {
	for {
		select {
		case <-ctx.Done():
			return
		case ws, ok := <-resize:
			if !ok {
				return
			}
			_ = host.Resize(ctx, harp, int(ws.Cols), int(ws.Rows))
		}
	}
}

// envMap turns exec's "K=V" environment slice into the keyed form a PaneSpec
// takes. A later duplicate wins, matching exec's own last-wins rule, so the
// merge BuildEnv performed survives the conversion instead of being reversed
// by it.
func envMap(env []string) map[string]string {
	if len(env) == 0 {
		return nil
	}
	out := make(map[string]string, len(env))
	for _, kv := range env {
		if k, v, ok := strings.Cut(kv, "="); ok {
			out[k] = v
		}
	}
	return out
}
