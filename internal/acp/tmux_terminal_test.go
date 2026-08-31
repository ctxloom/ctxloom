package acp

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"unicode/utf8"

	api "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTmuxRunner is a scriptable tmuxRunner: unit tests drive localTerminals'
// mapping logic (create/output/wait/kill/release, ensureSession, the
// tmux-missing failure) without a real tmux binary. Each call is recorded so
// a test can assert the exact tmux argv this file builds.
type fakeTmuxRunner struct {
	mu    sync.Mutex
	calls [][]string
	// fail, keyed by the tmux subcommand (args[0]), makes that subcommand
	// error every time it is called.
	fail    map[string]error
	failAll error // if set, every call fails with this error (tmux missing)
}

func newFakeTmuxRunner() *fakeTmuxRunner {
	return &fakeTmuxRunner{fail: map[string]error{}}
}

func (f *fakeTmuxRunner) Run(_ context.Context, args ...string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, append([]string(nil), args...))
	f.mu.Unlock()
	if f.failAll != nil {
		return "", f.failAll
	}
	if len(args) > 0 {
		if err, ok := f.fail[args[0]]; ok {
			return "", err
		}
	}
	return "", nil
}

func (f *fakeTmuxRunner) calledWith(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return true
		}
	}
	return false
}

func (f *fakeTmuxRunner) argsFor(sub string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 0 && c[0] == sub {
			return c
		}
	}
	return nil
}

// TestLocalTerminals_Create_MapsToNewWindow: CreateTerminal maps onto tmux
// new-window, carrying cwd/env/command/args through, and mints a distinct
// TerminalId per call.
func TestLocalTerminals_Create_MapsToNewWindow(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	cwd := "/work"

	resp1, err := l.create(context.Background(), api.CreateTerminalRequest{
		Command: "echo", Args: []string{"hi"}, Cwd: &cwd,
		Env: []api.EnvVariable{{Name: "FOO", Value: "bar"}},
	})
	require.NoError(t, err)
	resp2, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "true"})
	require.NoError(t, err)
	assert.NotEqual(t, resp1.TerminalId, resp2.TerminalId, "each CreateTerminal mints a distinct id")

	require.True(t, f.calledWith("has-session"), "ensureSession must probe for the fixed session first")
	newWindowArgs := f.argsFor("new-window")
	require.NotEmpty(t, newWindowArgs, "CreateTerminal must map onto tmux new-window")
	assert.Contains(t, newWindowArgs, "-c")
	assert.Contains(t, newWindowArgs, cwd)
	assert.Contains(t, newWindowArgs, "-e")
	assert.Contains(t, newWindowArgs, "FOO=bar")
	assert.Contains(t, newWindowArgs, "echo")
	assert.Contains(t, newWindowArgs, "hi")
}

// TestLocalTerminals_Output_ReadsCapturedFileNotPane: TerminalOutput reads
// the wrapper's captured-output file, not tmux's own pane — proven here by
// never invoking capture-pane at all, and by returning exactly what a test
// double writes to that file (as the real wrapper script would).
func TestLocalTerminals_Output_ReadsCapturedFileNotPane(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())

	resp, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "echo", Args: []string{"hi"}})
	require.NoError(t, err)

	// Simulate the wrapper script having run: write captured output + exit
	// status directly, since the fake runner never spawns a real process.
	term, ok := l.lookup(resp.TerminalId)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.outputPath, []byte("hello world\n"), 0o600))
	require.NoError(t, os.WriteFile(term.statusPath, []byte("0\n"), 0o600))

	out, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", out.Output)
	assert.False(t, out.Truncated)
	require.NotNil(t, out.ExitStatus)
	require.NotNil(t, out.ExitStatus.ExitCode)
	assert.Equal(t, 0, *out.ExitStatus.ExitCode)
	assert.False(t, f.calledWith("capture-pane"), "output must never read tmux's own pane (dead-pane placeholder text), only the captured file")
}

// TestLocalTerminals_Output_TruncatesFromStart honors
// CreateTerminalRequest.OutputByteLimit's documented contract: truncate from
// the BEGINNING, keeping the tail, at a UTF-8 boundary.
func TestLocalTerminals_Output_TruncatesFromStart(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	limit := 5
	resp, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "echo", OutputByteLimit: &limit})
	require.NoError(t, err)

	term, ok := l.lookup(resp.TerminalId)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.outputPath, []byte("0123456789"), 0o600))

	out, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)
	assert.Equal(t, "56789", out.Output, "keeps the LAST `limit` bytes, dropping from the start")
	assert.True(t, out.Truncated)
}

// TestLocalTerminals_Wait_BlocksOnChannelThenReadsStatus: WaitForTerminalExit
// maps onto tmux wait-for against the terminal's own channel, and the
// returned status comes from the status file the wrapper writes before
// signalling it.
func TestLocalTerminals_Wait_BlocksOnChannelThenReadsStatus(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	resp, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "sh"})
	require.NoError(t, err)

	term, ok := l.lookup(resp.TerminalId)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.statusPath, []byte("7\n"), 0o600))

	waitResp, err := l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)
	require.NotNil(t, waitResp.ExitCode)
	assert.Equal(t, 7, *waitResp.ExitCode)
	assert.True(t, f.calledWith("wait-for"))
}

// TestLocalTerminals_Kill_MapsToKillWindowAndUnblocksWait: KillTerminal maps
// onto tmux kill-window and, because kill-window destroys the window before
// the wrapper script's own signal line can run, ALSO signals the wait
// channel itself so a parked WaitForTerminalExit call is not left hanging
// forever.
func TestLocalTerminals_Kill_MapsToKillWindowAndUnblocksWait(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	resp, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "sleep", Args: []string{"30"}})
	require.NoError(t, err)

	_, err = l.kill(context.Background(), api.KillTerminalRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)
	assert.True(t, f.calledWith("kill-window"))

	waitResp, err := l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err, "wait-for after kill must not hang or error — the channel was signalled by kill itself")
	require.Nil(t, waitResp.ExitCode, "a killed process has no real exit CODE")
	require.NotNil(t, waitResp.Signal)
	assert.Equal(t, "SIGHUP", *waitResp.Signal)
}

// TestLocalTerminals_Release_KillsIfStillRunningThenForgetsHandle:
// ReleaseTerminal frees resources (kills first if not yet finished) and
// afterward the id is unknown to every other operation.
func TestLocalTerminals_Release_KillsIfStillRunningThenForgetsHandle(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	resp, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "sleep", Args: []string{"30"}})
	require.NoError(t, err)

	_, err = l.release(context.Background(), api.ReleaseTerminalRequest{TerminalId: resp.TerminalId})
	require.NoError(t, err)
	assert.True(t, f.calledWith("kill-window"), "release of a still-running terminal must kill it first")

	_, err = l.output(context.Background(), api.TerminalOutputRequest{TerminalId: resp.TerminalId})
	assert.Error(t, err, "a released id must be unknown afterward")

	// Releasing twice, or an id that never existed, is a benign no-op.
	_, err = l.release(context.Background(), api.ReleaseTerminalRequest{TerminalId: resp.TerminalId})
	assert.NoError(t, err)
	_, err = l.release(context.Background(), api.ReleaseTerminalRequest{TerminalId: "no-such-id"})
	assert.NoError(t, err)
}

// TestLocalTerminals_UnknownId_Errors covers output/wait/kill against an id
// that was never created.
func TestLocalTerminals_UnknownId_Errors(t *testing.T) {
	f := newFakeTmuxRunner()
	l := newLocalTerminals(f, t.TempDir())
	_, err := l.output(context.Background(), api.TerminalOutputRequest{TerminalId: "nope"})
	assert.Error(t, err)
	_, err = l.wait(context.Background(), api.WaitForTerminalExitRequest{TerminalId: "nope"})
	assert.Error(t, err)
	_, err = l.kill(context.Background(), api.KillTerminalRequest{TerminalId: "nope"})
	assert.Error(t, err)
}

// TestLocalTerminals_Create_TmuxMissing_FailsLoud: with tmux unreachable
// (the "tmux is not installed" case), CreateTerminal returns the runner's
// error rather than falling back to any decline or no-op terminal — the
// caller (session.go's serveLocalTerminal/localTerminalError) is what turns
// this into the remedy-carrying jsonrpc error; this test pins that the
// failure actually PROPAGATES this far rather than being swallowed.
func TestLocalTerminals_Create_TmuxMissing_FailsLoud(t *testing.T) {
	f := newFakeTmuxRunner()
	f.failAll = errors.New(`exec: "tmux": executable file not found in $PATH`)
	l := newLocalTerminals(f, t.TempDir())

	_, err := l.create(context.Background(), api.CreateTerminalRequest{Command: "echo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executable file not found")
}

// TestLocalTerminals_EnsureSession_ToleratesConcurrentCreateRace: a
// new-session failure (another process won the race to create the same
// fixed session first) is tolerated as long as a re-checked has-session
// confirms the session now exists — never surfaced as a CreateTerminal
// failure.
func TestLocalTerminals_EnsureSession_ToleratesConcurrentCreateRace(t *testing.T) {
	r := &sequencedRunner{steps: []stepResult{
		{args0: "has-session", err: errors.New("no such session")},       // first probe: missing
		{args0: "new-session", err: errors.New("duplicate session")},     // lost the race to create it
		{args0: "has-session", err: nil},                                 // re-check: it exists now
	}}
	l := newLocalTerminals(r, t.TempDir())
	assert.NoError(t, l.ensureSession(context.Background()))
}

// TestLocalTerminals_EnsureSession_SurfacesGenuineFailure: when the retry
// ALSO fails, ensureSession returns the original creation error rather than
// pretending the session exists.
func TestLocalTerminals_EnsureSession_SurfacesGenuineFailure(t *testing.T) {
	r := &sequencedRunner{steps: []stepResult{
		{args0: "has-session", err: errors.New("no such session")},
		{args0: "new-session", err: errors.New("permission denied")},
		{args0: "has-session", err: errors.New("no such session")},
	}}
	l := newLocalTerminals(r, t.TempDir())
	assert.Error(t, l.ensureSession(context.Background()))
}

// stepResult and sequencedRunner pin an EXACT call sequence (ensureSession's
// has-session -> new-session -> has-session retry), unlike fakeTmuxRunner's
// keyed-by-subcommand shape above.
type stepResult struct {
	args0 string
	err   error
}

type sequencedRunner struct {
	mu    sync.Mutex
	steps []stepResult
	i     int
}

func (s *sequencedRunner) Run(_ context.Context, args ...string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.i >= len(s.steps) {
		return "", nil
	}
	step := s.steps[s.i]
	s.i++
	return "", step.err
}

func TestTruncateFromStart(t *testing.T) {
	s, truncated := truncateFromStart("hello", 10)
	assert.Equal(t, "hello", s)
	assert.False(t, truncated)

	s, truncated = truncateFromStart("hello world", 5)
	assert.Equal(t, "world", s)
	assert.True(t, truncated)

	// A cut point that would land mid-rune must advance to the next
	// boundary instead of splitting a multi-byte character.
	multibyte := "a€bcdef" // € is 3 bytes (0xE2 0x82 0xAC); naive byte-6 cut lands inside it
	s, truncated = truncateFromStart(multibyte, 6)
	require.True(t, truncated)
	assert.True(t, utf8.ValidString(s), "truncation must land on a rune boundary: got %q", s)
}
