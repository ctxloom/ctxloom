package runtime_test

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/engines/mock"
	"github.com/ctxloom/ctxloom/internal/engines/mock/runtime"
)

// claudeInteractive resolves claude's interactive surface off L1, the same way
// claudeOneshot does for the oneshot one.
func claudeInteractive(t *testing.T) agent.EngineCLI {
	t.Helper()
	clis, ok := engines.EngineCLIs("claude-code")
	if !ok {
		t.Fatal("claude-code declares no engine CLIs")
	}
	cli, ok := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)
	if !ok {
		t.Fatal("claude-code declares no interactive surface")
	}
	return cli
}

// syncBuf is a goroutine-safe writer: the interactive loop writes from Run's
// goroutine while the test reads the accumulated output after it returns.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// interactiveRun is one in-process interactive session: the runtime on the
// other end of a pipe (its stdin) and a resize channel, with its exit code
// delivered when Run returns.
type interactiveRun struct {
	stdin  io.WriteCloser
	resize chan agent.WindowSize
	stdout *syncBuf
	stderr *syncBuf
	done   chan int
}

func startInteractive(t *testing.T, vendorArgv []string) *interactiveRun {
	t.Helper()
	return startInteractiveOn(t, claudeInteractive(t), vendorArgv)
}

// startInteractiveOn is startInteractive against an explicit L1 grammar.
func startInteractiveOn(t *testing.T, cli agent.EngineCLI, vendorArgv []string) *interactiveRun {
	t.Helper()
	parsed, err := cli.ParseArgv(vendorArgv)
	if err != nil {
		t.Fatalf("parse argv %v: %v", vendorArgv, err)
	}
	pr, pw := io.Pipe()
	s := &interactiveRun{
		stdin:  pw,
		resize: make(chan agent.WindowSize),
		stdout: &syncBuf{},
		stderr: &syncBuf{},
		done:   make(chan int, 1),
	}
	rt := &runtime.Runtime{
		CLI:       cli,
		Argv:      parsed,
		Res:       runtime.Resolver{Cwd: t.TempDir(), Home: t.TempDir(), Getenv: func(string) string { return "" }},
		Getenv:    func(string) string { return "" },
		LookupEnv: func(string) (string, bool) { return "", false },
		Stdin:     pr,
		Stdout:    s.stdout,
		Stderr:    s.stderr,
		Resize:    s.resize,
	}
	go func() { s.done <- rt.Run() }()
	t.Cleanup(func() { _ = pw.Close() })
	return s
}

// interactiveBound caps every wait on the in-process session. Each exchange
// with it is a pipe write, a channel send or a poll, and each of those blocks
// for as long as the session is not listening — forever, once a defect ends
// or wedges its loop. The bound turns that into a failure the test reports.
const interactiveBound = 5 * time.Second

// waitFor deadline-polls the captured stdout until it contains want — a
// bounded short-interval poll, never a bare sleep as the synchronization.
func (s *interactiveRun) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(interactiveBound)
	for !strings.Contains(s.stdout.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("stdout never contained %q; stdout:\n%s", want, s.stdout.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// exitCode waits (bounded) for Run to return and yields its exit code. A
// session that does not end is a failure, never a hang: the mock's whole
// point on this surface is that a test can close the turn on purpose.
func (s *interactiveRun) exitCode(t *testing.T) int {
	t.Helper()
	select {
	case code := <-s.done:
		return code
	case <-time.After(interactiveBound):
		t.Fatalf("the session did not end; stdout:\n%s", s.stdout.String())
		return -1
	}
}

// typeLine types one line at the session, bounded: an io.Pipe write returns
// only once the session reads it, so a session that has stopped reading would
// otherwise park the test. The abandoned write is released by the cleanup
// that closes the pipe.
func (s *interactiveRun) typeLine(t *testing.T, line string) {
	t.Helper()
	written := make(chan error, 1)
	go func() {
		_, err := io.WriteString(s.stdin, line+"\n")
		written <- err
	}()
	select {
	case err := <-written:
		if err != nil {
			t.Fatalf("write %q to the mock's stdin: %v", line, err)
		}
	case <-time.After(interactiveBound):
		t.Fatalf("the session never read %q; stdout:\n%s", line, s.stdout.String())
	}
}

// resizeTo delivers one resize, bounded for the same reason typeLine is: the
// channel is unbuffered, so the send completes only when the session takes it.
func (s *interactiveRun) resizeTo(t *testing.T, ws agent.WindowSize) {
	t.Helper()
	select {
	case s.resize <- ws:
	case <-time.After(interactiveBound):
		t.Fatalf("the session never took resize %dx%d; stdout:\n%s", ws.Rows, ws.Cols, s.stdout.String())
	}
}

// TestRuntime_Interactive_PromptIsTheTrailingPositional proves the interactive
// surface reads its prompt where L1 says the driver puts it — the trailing argv
// positional, never stdin — and answers it on the wire before the echo loop
// starts. The report must carry that prompt's hash so a delivery test can
// assert the bytes, not just that something arrived.
func TestRuntime_Interactive_PromptIsTheTrailingPositional(t *testing.T) {
	const prompt = "open the session with this"
	s := startInteractive(t, []string{"--name", "harp-x", prompt})
	s.typeLine(t, runtime.InteractiveQuit)
	code := s.exitCode(t)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, s.stderr.String())
	}
	if !strings.Contains(s.stdout.String(), "mock-engine: ok") {
		t.Errorf("the positional prompt was not answered on the wire; stdout:\n%s", s.stdout.String())
	}
	rep, err := runtime.ExtractReport(s.stderr.String())
	if err != nil {
		t.Fatalf("extract report: %v\nstderr:\n%s", err, s.stderr.String())
	}
	if rep.Surface != string(agent.CLISurfaceInteractive) {
		t.Errorf("report surface = %q, want %q", rep.Surface, agent.CLISurfaceInteractive)
	}
	if !rep.PromptPresent || rep.PromptSHA256 != sha256hex([]byte(prompt)) {
		t.Errorf("report prompt = present:%v sha:%s, want the positional's hash %s",
			rep.PromptPresent, rep.PromptSHA256, sha256hex([]byte(prompt)))
	}
}

// TestRuntime_Interactive_EchoesTypedLinesAndReportsResizes proves the two
// behaviours a real interactive engine has that a oneshot never does: it
// reflects what is typed at it, and it reacts to a terminal resize. The resize
// is reported the moment the mock sees it — not folded into the next echo —
// so once its line has been waited for, the echo of a line typed afterwards
// must follow it, and a test never has to guess how long a SIGWINCH takes.
func TestRuntime_Interactive_EchoesTypedLinesAndReportsResizes(t *testing.T) {
	s := startInteractive(t, []string{"--name", "harp-x", "hello"})
	s.typeLine(t, "ping")
	s.waitFor(t, runtime.InteractiveEchoPrefix+"ping\n")
	s.resizeTo(t, agent.WindowSize{Rows: 30, Cols: 100})
	s.waitFor(t, runtime.InteractiveWinsizePrefix+"30x100\n")
	s.typeLine(t, "pong")
	s.typeLine(t, runtime.InteractiveQuit)
	code := s.exitCode(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, s.stderr.String())
	}

	out := s.stdout.String()
	echo1 := strings.Index(out, runtime.InteractiveEchoPrefix+"ping\n")
	ws := strings.Index(out, runtime.InteractiveWinsizePrefix+"30x100\n")
	echo2 := strings.Index(out, runtime.InteractiveEchoPrefix+"pong\n")
	if echo1 < 0 || ws < 0 || echo2 < 0 {
		t.Fatalf("missing echo/winsize lines (ping=%d winsize=%d pong=%d); stdout:\n%s", echo1, ws, echo2, out)
	}
	if echo1 >= ws || ws >= echo2 {
		t.Errorf("order: ping-echo@%d winsize@%d pong-echo@%d; want ping < winsize < pong\n%s", echo1, ws, echo2, out)
	}
}

// TestRuntime_Interactive_EOFEndsTheSession: a closed stdin ends the turn with
// the outcome's exit code, so a driver that hangs up never leaves the mock
// parked forever.
func TestRuntime_Interactive_EOFEndsTheSession(t *testing.T) {
	s := startInteractive(t, []string{"hello"})
	if err := s.stdin.Close(); err != nil {
		t.Fatal(err)
	}
	if code := s.exitCode(t); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

// TestRuntime_Interactive_FailSentinelExitsNonzero: the sentinel dispatch is
// shared with oneshot — a fail sentinel in the positional prompt makes the
// interactive session exit nonzero after its (still emitted) report.
func TestRuntime_Interactive_FailSentinelExitsNonzero(t *testing.T) {
	s := startInteractive(t, []string{runtime.SentinelFail + " boom"})
	s.typeLine(t, runtime.InteractiveQuit)
	code := s.exitCode(t)
	if code == 0 {
		t.Fatalf("exit code = 0 on a fail sentinel; stdout:\n%s", s.stdout.String())
	}
	if _, err := runtime.ExtractReport(s.stderr.String()); err != nil {
		t.Errorf("report must be emitted even on a failing run: %v", err)
	}
}

// TestRuntime_Interactive_FiresTurnStartPerNonBlankLine: the interactive loop
// is the mock's stand-in for a TUI a human types at, so every non-blank line
// it reads is a prompt submitted — and a turn_start hook delivered to the
// session fires once for each, with the mock's payload on its stdin. Blank
// and whitespace-only lines are not prompts and fire nothing; the quit
// sentinel ends the session without firing.
//
// The hooks are the mock's own delivered hook file, named on argv by the
// mock kind's hooks flag; the grammar under test is the interactive surface
// extended with that flag.
func TestRuntime_Interactive_FiresTurnStartPerNonBlankLine(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "fired")
	hooksFile := filepath.Join(dir, "hooks.json")
	raw, err := json.Marshal(wire.UnifiedHooks{TurnStart: []wire.Hook{{Type: "command", Command: "cat >> " + marker + "; echo >> " + marker}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooksFile, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	cli := claudeInteractive(t)
	cli.Flags = append(cli.Flags, agent.CLIFlag{Name: mock.HooksFlag, Value: agent.ValuePath})
	s := startInteractiveOn(t, cli, []string{mock.HooksFlag, hooksFile, "hello"})
	s.typeLine(t, "one")
	s.waitFor(t, runtime.InteractiveEchoPrefix+"one\n")
	s.typeLine(t, "")
	s.typeLine(t, "   ")
	s.typeLine(t, "two")
	s.waitFor(t, runtime.InteractiveEchoPrefix+"two\n")
	s.typeLine(t, runtime.InteractiveQuit)
	if code := s.exitCode(t); code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, s.stderr.String())
	}

	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("the turn_start hook never ran: %v\nstderr:\n%s", err, s.stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(string(got)), "\n")
	if len(lines) != 2 {
		t.Fatalf("turn_start fired %d time(s), want 2 (one per non-blank line):\n%s", len(lines), got)
	}
	for _, line := range lines {
		var payload struct {
			Event string `json:"hook_event_name"`
		}
		if err := json.Unmarshal([]byte(line), &payload); err != nil || payload.Event != "turn_start" {
			t.Errorf("payload %q: event=%q err=%v; want turn_start", line, payload.Event, err)
		}
	}
}
