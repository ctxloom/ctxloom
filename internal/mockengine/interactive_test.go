package mockengine_test

import (
	"bytes"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// claudeInteractive resolves claude's interactive surface off L1, the same way
// claudeOneshot does for the oneshot one.
func claudeInteractive(t *testing.T) agent.EngineCLI {
	t.Helper()
	clis, ok := backends.EngineCLIsFor("claude-code")
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
	cli := claudeInteractive(t)
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
	rt := &mockengine.Runtime{
		CLI:       cli,
		Argv:      parsed,
		Res:       mockengine.Resolver{Cwd: t.TempDir(), Home: t.TempDir(), Getenv: func(string) string { return "" }},
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

// waitFor deadline-polls the captured stdout until it contains want — a
// bounded short-interval poll, never a bare sleep as the synchronization.
func (s *interactiveRun) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
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
	case <-time.After(5 * time.Second):
		t.Fatalf("the session did not end; stdout:\n%s", s.stdout.String())
		return -1
	}
}

func (s *interactiveRun) typeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.stdin, line+"\n"); err != nil {
		t.Fatalf("write %q to the mock's stdin: %v", line, err)
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
	s.typeLine(t, mockengine.InteractiveQuit)
	code := s.exitCode(t)

	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, s.stderr.String())
	}
	if !strings.Contains(s.stdout.String(), "mock-engine: ok") {
		t.Errorf("the positional prompt was not answered on the wire; stdout:\n%s", s.stdout.String())
	}
	rep, err := mockengine.ExtractReport(s.stderr.String())
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
	s.waitFor(t, mockengine.InteractiveEchoPrefix+"ping\n")
	s.resize <- agent.WindowSize{Rows: 30, Cols: 100}
	s.waitFor(t, mockengine.InteractiveWinsizePrefix+"30x100\n")
	s.typeLine(t, "pong")
	s.typeLine(t, mockengine.InteractiveQuit)
	code := s.exitCode(t)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0\nstderr:\n%s", code, s.stderr.String())
	}

	out := s.stdout.String()
	echo1 := strings.Index(out, mockengine.InteractiveEchoPrefix+"ping\n")
	ws := strings.Index(out, mockengine.InteractiveWinsizePrefix+"30x100\n")
	echo2 := strings.Index(out, mockengine.InteractiveEchoPrefix+"pong\n")
	if echo1 < 0 || ws < 0 || echo2 < 0 {
		t.Fatalf("missing echo/winsize lines (ping=%d winsize=%d pong=%d); stdout:\n%s", echo1, ws, echo2, out)
	}
	if !(echo1 < ws && ws < echo2) {
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
	s := startInteractive(t, []string{mockengine.SentinelFail + " boom"})
	s.typeLine(t, mockengine.InteractiveQuit)
	code := s.exitCode(t)
	if code == 0 {
		t.Fatalf("exit code = 0 on a fail sentinel; stdout:\n%s", s.stdout.String())
	}
	if _, err := mockengine.ExtractReport(s.stderr.String()); err != nil {
		t.Errorf("report must be emitted even on a failing run: %v", err)
	}
}
