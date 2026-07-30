package isolation

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/stderrtail"
)

// AttachedContainer is a running container's foreground stdio for a caller
// that speaks its OWN protocol directly with the in-container process — as
// opposed to SpawnClient's go-plugin-over-socket transport, which serves the
// `ctxloom llm serve` gRPC protocol. The ACP client driver's container
// transport (ISO1, internal/acp) is the first such caller: plain JSON-RPC
// rides Stdin/Stdout directly, no go-plugin handshake involved, so the
// heavier RunnerFunc/AddrTranslator machinery SpawnClient uses would be pure
// overhead here — this is the minimal primitive underneath it (exec the
// runtime's `run` argv, no daemon-specific socket dance).
type AttachedContainer struct {
	Stdin  io.WriteCloser
	Stdout io.Reader
	// ShutdownGrace bounds how long Close waits for the in-container process to
	// exit ON ITS OWN after stdin EOF, before force-removing the container.
	// Zero means DefaultShutdownGrace. Set it before Close; a caller that knows
	// its protocol ends the conversation by closing stdin (the ACP driver) needs
	// this for the same reason its host path does — see DefaultShutdownGrace.
	ShutdownGrace time.Duration
	close         func(grace time.Duration) error
	stderr        *stderrtail.Ring
}

// DefaultShutdownGrace is how long an attached container gets to exit on its own
// after stdin closes before teardown force-removes it. The engine adapters that
// ride this transport flush their own NATIVE session transcript asynchronously
// once the conversation ends, and a zero-grace removal races that flush and
// truncates it — measured on the HOST path first (internal/acp's identical
// constant, tidy-gush) and true for exactly the same reason in a container,
// where the removal is even less forgiving: `docker rm -f` takes the whole
// filesystem the half-written transcript lives on with it.
const DefaultShutdownGrace = 3 * time.Second

// attachWaitDelay bounds how long cmd.Wait may spend draining the container's
// I/O after the `run` client process is gone (os/exec's Cmd.WaitDelay). Short:
// by the time it applies, teardown has already force-killed the client and the
// only thing left to wait for is a pipe an orphan may still hold open.
const attachWaitDelay = time.Second

// StderrTail is the bounded tail of everything the CONTAINER wrote to stderr,
// captured as it streamed. For the ACP container transport this is the engine
// adapter's own dying words — the only root-cause evidence a caller ever gets
// when the in-container process exits before answering a JSON-RPC call, and
// the reason this is captured rather than merely inherited: Close force-
// removes the container, so `docker logs` afterwards has nothing to read.
func (a *AttachedContainer) StderrTail() string { return a.stderr.Tail() }

// Close tears the container down. Safe to call once.
func (a *AttachedContainer) Close() error {
	grace := a.ShutdownGrace
	if grace <= 0 {
		grace = DefaultShutdownGrace
	}
	return a.close(grace)
}

// interactiveRunArgs inserts `-i` right after RunArgs' leading "run" element,
// keeping the container's stdin OPEN for this caller's piped writes. The
// shared RunSpec/RunArgs never add it themselves: the go-plugin transport
// (containerRunnerFunc/newContainerRunner) deliberately leaves stdin nil —
// go-plugin never watches it, so `-i` would only hold the run process open
// for nothing — but a caller speaking its OWN stdio protocol (RunAttached's
// whole reason to exist) needs a live bidirectional stdin or the container
// sees EOF immediately and the engine's read loop exits before ever seeing a
// request. A container runtime's args start with "run", so element 1 is the
// insertion point — but NOT every Runtime renders a run argv at all: Host
// launches no container and returns nil, so an empty argv has no insertion
// point and is returned untouched (RunAttached refuses such a runtime before
// it ever gets here).
func interactiveRunArgs(args []string) []string {
	if len(args) == 0 {
		return args
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, args[0], "-i")
	return append(out, args[1:]...)
}

// RunAttached starts spec's container in the FOREGROUND (Runtime.RunArgs —
// stdout/stderr attached, no -d/-t: a pty would mangle a piped protocol
// exactly as it would the go-plugin handshake) with piped stdin/stdout for a
// caller that will speak its own protocol with the in-container process.
//
// Close force-removes the container by NAME — mirrors containerRunner.Kill's
// doc (runner.go): merely killing the local `run` CLI process would not stop
// a container that raced ahead of us (no --rm, or a daemon detached from the
// client), and a wedged daemon must not hang teardown forever, so the remove
// runs under containerRemoveTimeout regardless of the caller's own ctx. A
// remove that reports the container already gone (the benign --rm race) is
// teardown SUCCESS, not a leak (removeReportsGone) — only a genuine failure
// to confirm removal is surfaced, loudly, since the live container would
// otherwise hold this session's workspace mounts invisibly.
func RunAttached(ctx context.Context, rt Runtime, spec RunSpec) (*AttachedContainer, error) {
	// Host (and any runtime that launches no container) renders no run argv at
	// all. Refusing here names the actual problem; without it the empty argv
	// reached interactiveRunArgs' index, or — past that — exec'd the runtime
	// binary bare.
	runArgs := rt.RunArgs(spec)
	if len(runArgs) == 0 {
		return nil, fmt.Errorf("container attach: runtime %q cannot start a container (it renders no run argv)", rt.Name())
	}
	cmd := exec.CommandContext(ctx, rt.Binary(), interactiveRunArgs(runArgs)...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("container attach: stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("container attach: stdout pipe: %w", err)
	}
	// Tee, never replace: the passthrough to the host's stderr is an
	// operator-facing diagnostic some callers already rely on, and capture
	// must be ADDITIVE — the whole point is that the evidence stops
	// vanishing, not that it moves somewhere else.
	ring, sink := stderrtail.TeeStderr(stderrtail.DefaultBytes)
	cmd.Stderr = sink
	// The stderr tee is not an *os.File, so os/exec copies it through a pipe of
	// its own and cmd.Wait blocks until EVERY holder of that pipe's write end
	// closes it — including any process the container's entrypoint orphaned. Wait
	// must not be able to outlive the container it is waiting on, so I/O drain is
	// bounded too: without this, Close's force-kill of the `run` client can be
	// followed by an unbounded Wait.
	cmd.WaitDelay = attachWaitDelay
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s container: %w", rt.Name(), err)
	}
	name := spec.Name
	// waitErr/processDone let Close observe a natural exit without a second
	// cmd.Wait (os/exec forbids more than one caller) and without blocking
	// teardown forever on a container that ignores stdin closing.
	var waitErr error
	processDone := make(chan struct{})
	go func() {
		waitErr = cmd.Wait()
		close(processDone)
	}()
	return &AttachedContainer{
		Stdin:  stdin,
		Stdout: stdout,
		stderr: ring,
		close: func(grace time.Duration) error {
			_ = stdin.Close()
			// Bounded grace for the in-container process to finish on its own
			// (see DefaultShutdownGrace): everything below this select is
			// destructive, and the async transcript flush it would race is
			// unrecoverable once the container is removed.
			select {
			case <-processDone:
			case <-time.After(grace):
			}
			if name != "" && rt.Binary() != "" {
				cctx, cancel := context.WithTimeout(context.Background(), containerRemoveTimeout)
				if _, rerr := probeExec(cctx, rt.Binary(), rt.RemoveArgs(name)); rerr != nil && !removeReportsGone(rerr) {
					clidiag.Warn("ctxloom",
						"container %q may still be running after teardown (%v) — the %s daemon did not confirm removal; it holds this session's workspace, remove it manually with `%s %s`",
						name, rerr, rt.Name(), rt.Binary(), strings.Join(rt.RemoveArgs(name), " "))
				}
				cancel()
			}
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			<-processDone
			return waitErr
		},
	}, nil
}
