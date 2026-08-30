//go:build !windows

package coord

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/term"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/shared/ptyrunner"
)

// wakeProbeEnv marks the re-executed test binary as the child under the pty
// rather than the parent driving it.
const wakeProbeEnv = "CTXLOOM_WAKE_STDIN_PROBE"

// Probe markers. The child reports its own read boundaries out through the
// pty, which is the only vantage point from which the question "did the
// submit arrive in its own read?" can actually be answered: every observation
// point above the pty master is a place where the split might still be intact
// and be destroyed later.
const (
	probeReady = "PROBE-READY"
	probeRead  = "PROBE-READ "
	probeDone  = "PROBE-DONE"
)

// TestHelperWakeStdinProbe is not a test. It is the child process the byte-path
// probe runs under a real pty, standing in for the engine (claude) whose stdin
// the wake is injected into. It puts the terminal in RAW mode first, exactly as
// an interactive engine does, because the tty line discipline in its default
// cooked mode re-frames reads on line boundaries and translates CR to NL —
// which would manufacture the very split the probe is trying to detect.
func TestHelperWakeStdinProbe(t *testing.T) {
	if os.Getenv(wakeProbeEnv) != "1" {
		t.Skip("helper process for TestTerminalInject_SubmitReachesEngineStdinAsItsOwnRead")
	}
	fd := int(os.Stdin.Fd())
	restore, err := term.MakeRaw(fd)
	if err != nil {
		fmt.Printf("PROBE-ERR %v\r\n", err)
		return
	}
	defer func() { _ = term.Restore(fd, restore) }()

	// Raw mode has cleared OPOST, so every line ending below is written
	// explicitly as CRLF.
	fmt.Print(probeReady + "\r\n")

	buf := make([]byte, 4096)
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n, rerr := os.Stdin.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			fmt.Printf("%s%s\r\n", probeRead, strconv.Quote(chunk))
			if strings.Contains(chunk, "\r") {
				break
			}
		}
		if rerr != nil {
			break
		}
	}
	fmt.Print(probeDone + "\r\n")
}

// TestTerminalInject_SubmitReachesEngineStdinAsItsOwnRead is the byte-path
// measurement: it drives the REAL injector through the REAL interactive
// consumer (ptyrunner.RunInteractive, which internal/lm/backends hands every
// interactive engine) into a REAL pty, and reads back the read boundaries the
// child process actually observed on its stdin.
//
// The unit test above it (TestNudgeReader_SubmitIsADistinctLaterReadEvent)
// proves only that the READER returns the frame and the submit separately.
// That is not the property the wake depends on. What the engine's TUI reacts
// to is how the bytes land on ITS stdin, several hops lower — and any buffered
// reader or accumulate-then-write copier between the two would rejoin them
// while leaving the reader's own test green. This closes that gap by measuring
// the far end.
func TestTerminalInject_SubmitReachesEngineStdinAsItsOwnRead(t *testing.T) {
	h := newNoticeHome(t)
	ti := &TerminalInjector{
		quiet:   20 * time.Millisecond,
		tick:    time.Millisecond,
		maxWait: 2 * time.Second,
		ackWait: time.Minute,
		ackTick: time.Second,
		count:   h.BufferedMailCount,
	}
	h.SetTerminalNudge(ti.nudge)

	// A real stdin nobody is typing into, which is the whole point: the wake
	// has to actuate a session that is producing no input of its own.
	realStdin, realStdinW := io.Pipe()
	t.Cleanup(func() { _ = realStdinW.Close() })

	out := &probeSink{}
	// Exactly what llm_serve.go's wrapStreams closure does per turn.
	wrappedStdin, wrappedStdout := ti.Wrap(realStdin, out)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperWakeStdinProbe$")
	cmd.Env = append(os.Environ(), wakeProbeEnv+"=1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		// realStdin's owner supplies its release, exactly as GRPCServer.Run
		// does for the pipe it makes: the wrapped reader parks a pump
		// goroutine in realStdin.Read, and closing the underlying pipe is the
		// only thing that retires it.
		_, _ = ptyrunner.RunInteractive(ctx, cmd, wrappedStdin, func() { _ = realStdin.Close() }, wrappedStdout, nil)
	}()

	// Do not inject until the child holds the terminal in raw mode, or the
	// cooked line discipline would frame the reads instead of the producer.
	requireProbeMarker(t, out, probeReady, 30*time.Second)

	h.deliverNotice(&agentcoordpb.PeerMessage{MessageId: "m-1", Text: "for the terminal"})

	requireProbeMarker(t, out, probeDone, 30*time.Second)
	cancel()
	<-done

	reads := probeReads(t, out.String())
	for i, r := range reads {
		t.Logf("engine stdin read %d/%d: %q", i+1, len(reads), r)
	}
	require.GreaterOrEqual(t, len(reads), 2,
		"the engine's stdin saw %d read(s); the frame and its submit must arrive as SEPARATE reads, and a single read is the shape that renders the carriage return as literal text and never submits: %q",
		len(reads), reads)

	last := reads[len(reads)-1]
	assert.Equal(t, "\r", last,
		"the final read on the engine's stdin must be the bare carriage return that submits the frame; got %q. Anything else means something between nudgeReader.Read and the pty rejoined the two reads", last)

	frame := strings.Join(reads[:len(reads)-1], "")
	assert.Contains(t, frame, "call agent_recv", "the frame the engine actually received must be the mail-pending directive")
	assert.NotContains(t, frame, "\r",
		"no carriage return may ride along with the frame's own reads: a TUI treats one read as a paste and renders the CR as text")
}

// requireProbeMarker waits for marker to appear in the child's output.
func requireProbeMarker(t *testing.T, out *probeSink, marker string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(out.String(), marker) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child never reported %s within %s; output so far:\n%s", marker, d, out.String())
}

// probeReads extracts, in order, the chunks the child reported as individual
// Read returns on its stdin.
func probeReads(t *testing.T, s string) []string {
	t.Helper()
	var got []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		_, rest, ok := strings.Cut(line, probeRead)
		if !ok {
			continue
		}
		chunk, err := strconv.Unquote(rest)
		require.NoError(t, err, "malformed probe line %q", line)
		got = append(got, chunk)
	}
	return got
}

// probeSink collects the pty's output; ptyrunner writes to it from its own
// goroutine while the test reads it.
type probeSink struct {
	mu sync.Mutex
	b  strings.Builder
}

func (p *probeSink) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.Write(b)
}

func (p *probeSink) String() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.b.String()
}
