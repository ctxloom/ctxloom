//go:build unix

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pty "github.com/aymanbagabas/go-pty"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/mockengine"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ptyWait bounds every deadline-poll in the pty test. Generous relative to a
// mock that answers in microseconds; the poll returns the moment the
// condition holds, so the budget is only spent on a failure.
const ptyWait = 10 * time.Second

// ptyCapture accumulates everything read off the pty master while the test
// polls it. Same idiom as tests/integration/testenv's ptyCapture; re-derived
// here because that one is unexported to a package this test cannot import
// without pulling the whole integration harness into a unit test binary.
type ptyCapture struct {
	mu sync.Mutex
	b  strings.Builder
}

func (c *ptyCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(p)
}

func (c *ptyCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.String()
}

// mockSession is the mock running as its own process on a real pty.
type mockSession struct {
	pty     pty.Pty
	cmd     *pty.Cmd
	out     *ptyCapture
	exited  chan struct{} // closed once Wait returns; exitErr is then readable
	exitErr error
}

// startMockOnPTY re-executes this test binary as the mock (see reexecEnv) on
// a fresh pty sized cols x rows, with the vendor argv given, HOME and cwd on
// temp dirs, and the report file channel pointed at reportPath.
func startMockOnPTY(t *testing.T, cols, rows int, reportPath string, args ...string) *mockSession {
	t.Helper()
	p, err := pty.New()
	if err != nil {
		t.Fatalf("open pty: %v", err)
	}
	if err := p.Resize(cols, rows); err != nil {
		t.Fatalf("resize pty: %v", err)
	}
	cmd := p.Command(os.Args[0], args...)
	cmd.Dir = t.TempDir()
	// TERM=dumb for the same reason testenv.RunPTY fixes it: nothing here
	// answers a terminal's capability queries.
	cmd.Env = append(os.Environ(),
		reexecEnv+"=1",
		"HOME="+t.TempDir(),
		"TERM=dumb",
		mockengine.EnvReportFile+"="+reportPath,
	)
	s := &mockSession{pty: p, cmd: cmd, out: &ptyCapture{}, exited: make(chan struct{})}
	go func() { _, _ = io.Copy(s.out, p) }()
	if err := cmd.Start(); err != nil {
		_ = p.Close()
		t.Fatalf("start mock on pty: %v", err)
	}
	go func() {
		s.exitErr = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Kill()
			<-s.exited
		}
		_ = p.Close()
	})
	return s
}

// typeLine writes a line into the pty as if typed at the terminal.
func (s *mockSession) typeLine(t *testing.T, line string) {
	t.Helper()
	if _, err := s.pty.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("type %q: %v", line, err)
	}
}

// waitFor deadline-polls the captured pty output until it contains want.
func (s *mockSession) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(ptyWait)
	for {
		if strings.Contains(s.out.String(), want) {
			return
		}
		select {
		case <-s.exited:
			// Give the copier a moment to drain what the exit left behind.
			time.Sleep(50 * time.Millisecond)
			if strings.Contains(s.out.String(), want) {
				return
			}
			t.Fatalf("mock exited (%v) before its output contained %q; output:\n%s", s.exitErr, want, s.out.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("pty output never contained %q within %v; output so far:\n%s", want, ptyWait, s.out.String())
		}
		time.Sleep(time.Millisecond)
	}
}

// waitExit blocks for the mock to exit and returns its exit code.
func (s *mockSession) waitExit(t *testing.T) int {
	t.Helper()
	select {
	case <-s.exited:
		return s.cmd.ProcessState.ExitCode()
	case <-time.After(ptyWait):
		t.Fatalf("mock did not exit within %v; output:\n%s", ptyWait, s.out.String())
		return -1
	}
}

// TestPTY_InteractiveSurface_EndToEnd drives the mock, selected as claude's
// interactive personality, over a REAL pty as its own process: the prompt
// travels as the trailing positional, the reply and the evidence report come
// back through the pty, a typed line is reflected, a resize of the pty master
// reaches the mock as SIGWINCH and is reported with the new geometry, and
// "quit" ends the session with exit 0. This is the whole interactive contract
// the in-process Runtime tests cannot reach, because signals and the tty line
// discipline only exist on a real pty.
func TestPTY_InteractiveSurface_EndToEnd(t *testing.T) {
	const prompt = "start here"
	reportPath := filepath.Join(t.TempDir(), "report.json")
	s := startMockOnPTY(t, 80, 24, reportPath,
		"--claude-code", "--surface", string(agent.CLISurfaceInteractive), "--name", "harp-pty", prompt)

	// The positional prompt is answered before anything is typed.
	s.waitFor(t, "mock-engine: ok")

	// The evidence report crossed the pty intact, \r\n and all.
	rep, err := mockengine.ExtractReport(s.out.String())
	if err != nil {
		t.Fatalf("extract report from pty output: %v\n%s", err, s.out.String())
	}
	sum := sha256.Sum256([]byte(prompt))
	if rep.Surface != string(agent.CLISurfaceInteractive) || rep.PromptSHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("report surface=%q promptSha256=%q; want interactive and the positional's hash %s",
			rep.Surface, rep.PromptSHA256, hex.EncodeToString(sum[:]))
	}

	s.typeLine(t, "ping")
	s.waitFor(t, mockengine.InteractiveEchoPrefix+"ping")

	// A resize of the master is a SIGWINCH to the mock; it reports the size
	// it read back off its own tty, so the numbers prove the ioctl round trip.
	if err := s.pty.Resize(100, 30); err != nil {
		t.Fatalf("resize pty: %v", err)
	}
	s.waitFor(t, mockengine.InteractiveWinsizePrefix+"30x100")

	s.typeLine(t, mockengine.InteractiveQuit)
	if code := s.waitExit(t); code != 0 {
		t.Fatalf("exit code = %d, want 0; output:\n%s", code, s.out.String())
	}

	// The file channel agrees with the pty channel.
	b, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("read report file: %v", err)
	}
	var fileRep mockengine.Report
	if err := json.Unmarshal(b, &fileRep); err != nil {
		t.Fatalf("report file did not parse: %v", err)
	}
	if fileRep.DiscoveryDigest != rep.DiscoveryDigest || fileRep.PromptSHA256 != rep.PromptSHA256 {
		t.Errorf("file report (digest %s, prompt %s) disagrees with the pty report (digest %s, prompt %s)",
			fileRep.DiscoveryDigest, fileRep.PromptSHA256, rep.DiscoveryDigest, rep.PromptSHA256)
	}
}

// TestRun_SurfaceSelection pins the surface selector: --surface names a
// declared surface, an undeclared one is a loud exit 2, and the default
// remains oneshot (whose grammar REQUIRES --print, so an interactive-shaped
// argv is refused rather than misread).
func TestRun_SurfaceSelection(t *testing.T) {
	t.Setenv(mockengine.EnvReportFile, "")
	if code := run([]string{"--claude-code", "--surface", "no-such-surface", "hello"}); code != 2 {
		t.Errorf("undeclared surface: exit %d, want 2", code)
	}
	if code := run([]string{"--claude-code", "--surface"}); code != 2 {
		t.Errorf("--surface without a value: exit %d, want 2", code)
	}
	// Default surface is oneshot: an argv without --print does not parse.
	if code := run([]string{"--claude-code", "hello"}); code != 2 {
		t.Errorf("interactive-shaped argv on the default (oneshot) surface: exit %d, want 2", code)
	}
}

// TestImpersonable_NamesExactlyTheBackendsWithAnEngineCLI pins the hint the
// no-personality refusal prints: every name it offers is selectable (declares
// an engine CLI), and every selectable backend is offered.
func TestImpersonable_NamesExactlyTheBackendsWithAnEngineCLI(t *testing.T) {
	offered := map[string]bool{}
	for _, name := range impersonable() {
		if _, ok := backends.EngineCLIsFor(name); !ok {
			t.Errorf("hint offers %q, which declares no engine CLI and so cannot be selected", name)
		}
		if _, ok := personalityFromFlag("--" + name); !ok {
			t.Errorf("hint offers %q, but --%s does not select it", name, name)
		}
		offered[name] = true
	}
	for _, name := range backends.List() {
		if _, ok := backends.EngineCLIsFor(name); ok && !offered[name] {
			t.Errorf("%q is selectable but the hint does not offer it", name)
		}
	}
	if len(offered) == 0 {
		t.Fatal("the hint offers nothing: this test checked no name")
	}
}
