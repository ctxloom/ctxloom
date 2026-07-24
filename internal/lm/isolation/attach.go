package isolation

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"

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
	close  func() error
	stderr *stderrtail.Ring
}

// StderrTail is the bounded tail of everything the CONTAINER wrote to stderr,
// captured as it streamed. For the ACP container transport this is the engine
// adapter's own dying words — the only root-cause evidence a caller ever gets
// when the in-container process exits before answering a JSON-RPC call, and
// the reason this is captured rather than merely inherited: Close force-
// removes the container, so `docker logs` afterwards has nothing to read.
func (a *AttachedContainer) StderrTail() string { return a.stderr.Tail() }

// Close tears the container down. Safe to call once.
func (a *AttachedContainer) Close() error { return a.close() }

// interactiveRunArgs inserts `-i` right after RunArgs' leading "run" element,
// keeping the container's stdin OPEN for this caller's piped writes. The
// shared RunSpec/RunArgs never add it themselves: the go-plugin transport
// (containerRunnerFunc/newContainerRunner) deliberately leaves stdin nil —
// go-plugin never watches it, so `-i` would only hold the run process open
// for nothing — but a caller speaking its OWN stdio protocol (RunAttached's
// whole reason to exist) needs a live bidirectional stdin or the container
// sees EOF immediately and the engine's read loop exits before ever seeing a
// request. Args always start with "run" (every Runtime.RunArgs
// implementation here does), so element 1 is always a safe insertion point.
func interactiveRunArgs(args []string) []string {
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
	cmd := exec.CommandContext(ctx, rt.Binary(), interactiveRunArgs(rt.RunArgs(spec))...)
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
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s container: %w", rt.Name(), err)
	}
	name := spec.Name
	return &AttachedContainer{
		Stdin:  stdin,
		Stdout: stdout,
		stderr: ring,
		close: func() error {
			_ = stdin.Close()
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
			return cmd.Wait()
		},
	}, nil
}
