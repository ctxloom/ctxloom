package tmuxhost

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeTmuxRunner is a scriptable Runner: unit tests drive Terminals'
// mapping logic (Create/Output/Wait/Kill/Release, ensureSession, the
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

// TestEnsureSession_ConfiguresAnADOPTEDServerToo: the remain-on-exit default
// must be applied whenever this process ensures the session — NOT only when it
// happens to be the process that CREATED it.
//
// The dedicated server is documented as throwaway but nothing tears it down, so
// a session routinely OUTLIVES the run that made it and the next run adopts it.
// If configuration only happens on the create path, an adopted server is left
// unconfigured and the second run behaves differently from the first — a
// difference that presents as a product defect while being purely environmental.
// Measured 2026-08-30: three leaked servers accumulated in one evening, and one
// of them turned a passing suite red.
func TestEnsureSession_ConfiguresAnAdoptedServerToo(t *testing.T) {
	f := newFakeTmuxRunner()
	// has-session SUCCEEDS: the session already exists, i.e. this process is
	// adopting a server some earlier run left behind.
	l := New(f, t.TempDir())

	_, err := l.Create(context.Background(), Spec{Command: "true"})
	require.NoError(t, err)

	require.True(t, f.calledWith("has-session"), "ensureSession still probes first")
	assert.Empty(t, f.argsFor("new-session"),
		"an existing session must not be re-created")
	opt := f.argsFor("set-option")
	require.NotEmpty(t, opt,
		"an ADOPTED server must still be configured — otherwise run N+1 inherits an unconfigured server")
	assert.Contains(t, opt, "remain-on-exit")
	assert.Contains(t, opt, "on")
}

// TestTerminals_NamesDoNotCollideAcrossProcesses: two Terminals —
// standing in for two ctxloom RUNS sharing the fixed tmux server — must not
// mint the same window name or terminal id.
//
// PROVEN CAUSE of the 30-minute hang (exposable-overturn): the name comes from
// Terminals.seq, a per-PROCESS counter that restarts at zero, so every
// run's first terminal is window "t1" on channel "ctxloom-acp-term-t1". tmux
// ALLOWS duplicate window names, so run 2's window is shadowed by run 1's
// leftover: kill-window and the wait target both become ambiguous, and run 2
// blocks forever on a channel its own window never signals. Measured: run 1
// green 3/3, run 2 panicked with "test timed out after 30m0s".
func TestTerminals_NamesDoNotCollideAcrossProcesses(t *testing.T) {
	f1, f2 := newFakeTmuxRunner(), newFakeTmuxRunner()
	l1 := New(f1, t.TempDir())
	l2 := New(f2, t.TempDir())

	id1, err := l1.Create(context.Background(), Spec{Command: "true"})
	require.NoError(t, err)
	id2, err := l2.Create(context.Background(), Spec{Command: "true"})
	require.NoError(t, err)

	assert.NotEqual(t, id1, id2,
		"two runs sharing one tmux server must not mint the same terminal id")

	win1, win2 := f1.argsFor("new-window"), f2.argsFor("new-window")
	require.NotEmpty(t, win1)
	require.NotEmpty(t, win2)
	assert.NotEqual(t, nameAfterFlag(win1, "-n"), nameAfterFlag(win2, "-n"),
		"tmux permits duplicate window names, so a collision shadows the older window and makes kill/wait targets ambiguous")
}

// nameAfterFlag returns the argument following flag, or "" if absent.
func nameAfterFlag(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// TestTerminals_Create_MapsToNewWindow: Create maps onto tmux new-window,
// carrying cwd/env/command/args through, and mints a distinct TerminalID per
// call.
func TestTerminals_Create_MapsToNewWindow(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	cwd := "/work"

	id1, err := l.Create(context.Background(), Spec{
		Command: "echo", Args: []string{"hi"}, Cwd: cwd,
		Env: []EnvVar{{Name: "FOO", Value: "bar"}},
	})
	require.NoError(t, err)
	id2, err := l.Create(context.Background(), Spec{Command: "true"})
	require.NoError(t, err)
	assert.NotEqual(t, id1, id2, "each Create mints a distinct id")

	require.True(t, f.calledWith("has-session"), "ensureSession must probe for the fixed session first")
	newWindowArgs := f.argsFor("new-window")
	require.NotEmpty(t, newWindowArgs, "Create must map onto tmux new-window")
	assert.Contains(t, newWindowArgs, "-c")
	assert.Contains(t, newWindowArgs, cwd)
	assert.Contains(t, newWindowArgs, "-e")
	assert.Contains(t, newWindowArgs, "FOO=bar")
	assert.Contains(t, newWindowArgs, "echo")
	assert.Contains(t, newWindowArgs, "hi")
}

// TestTerminals_Output_ReadsCapturedFileNotPane: Output reads the wrapper's
// captured-output file, not tmux's own pane — proven here by never invoking
// capture-pane at all, and by returning exactly what a test double writes to
// that file (as the real wrapper script would).
func TestTerminals_Output_ReadsCapturedFileNotPane(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())

	id, err := l.Create(context.Background(), Spec{Command: "echo", Args: []string{"hi"}})
	require.NoError(t, err)

	// Simulate the wrapper script having run: write captured output + exit
	// status directly, since the fake runner never spawns a real process.
	term, ok := l.lookup(id)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.outputPath, []byte("hello world\n"), 0o600))
	require.NoError(t, os.WriteFile(term.statusPath, []byte("0\n"), 0o600))

	out, err := l.Output(id)
	require.NoError(t, err)
	assert.Equal(t, "hello world\n", out.Text)
	assert.False(t, out.Truncated)
	require.NotNil(t, out.Exit)
	require.NotNil(t, out.Exit.ExitCode)
	assert.Equal(t, 0, *out.Exit.ExitCode)
	assert.False(t, f.calledWith("capture-pane"), "output must never read tmux's own pane (dead-pane placeholder text), only the captured file")
}

// TestTerminals_Output_TruncatesFromStart honors Spec.OutputLimit's documented
// contract: truncate from the BEGINNING, keeping the tail, at a UTF-8 boundary.
func TestTerminals_Output_TruncatesFromStart(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	limit := 5
	id, err := l.Create(context.Background(), Spec{Command: "echo", OutputLimit: &limit})
	require.NoError(t, err)

	term, ok := l.lookup(id)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.outputPath, []byte("0123456789"), 0o600))

	out, err := l.Output(id)
	require.NoError(t, err)
	assert.Equal(t, "56789", out.Text, "keeps the LAST `limit` bytes, dropping from the start")
	assert.True(t, out.Truncated)
}

// TestTerminals_Wait_BlocksOnChannelThenReadsStatus: Wait maps onto tmux
// wait-for against the terminal's own channel, and the returned status comes
// from the status file the wrapper writes before signalling it.
func TestTerminals_Wait_BlocksOnChannelThenReadsStatus(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	id, err := l.Create(context.Background(), Spec{Command: "sh"})
	require.NoError(t, err)

	term, ok := l.lookup(id)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.statusPath, []byte("7\n"), 0o600))

	st, err := l.Wait(context.Background(), id)
	require.NoError(t, err)
	require.NotNil(t, st)
	require.NotNil(t, st.ExitCode)
	assert.Equal(t, 7, *st.ExitCode)
	assert.True(t, f.calledWith("wait-for"))
}

// TestTerminals_Kill_MapsToKillWindowAndUnblocksWait: Kill maps onto tmux
// kill-window and, because kill-window destroys the window before the wrapper
// script's own signal line can run, ALSO signals the wait channel itself so a
// parked Wait call is not left hanging forever.
func TestTerminals_Kill_MapsToKillWindowAndUnblocksWait(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	id, err := l.Create(context.Background(), Spec{Command: "sleep", Args: []string{"30"}})
	require.NoError(t, err)

	require.NoError(t, l.Kill(context.Background(), id))
	assert.True(t, f.calledWith("kill-window"))

	st, err := l.Wait(context.Background(), id)
	require.NoError(t, err, "wait-for after kill must not hang or error — the channel was signalled by kill itself")
	require.NotNil(t, st)
	require.Nil(t, st.ExitCode, "a killed process has no real exit CODE")
	require.NotNil(t, st.Signal)
	assert.Equal(t, "SIGHUP", *st.Signal)
}

// TestTerminals_Release_KillsIfStillRunningThenForgetsHandle: Release frees
// resources (kills first if not yet finished) and afterward the id is unknown
// to every other operation.
func TestTerminals_Release_KillsIfStillRunningThenForgetsHandle(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	id, err := l.Create(context.Background(), Spec{Command: "sleep", Args: []string{"30"}})
	require.NoError(t, err)

	require.NoError(t, l.Release(context.Background(), id))
	assert.True(t, f.calledWith("kill-window"), "release of a still-running terminal must kill it first")

	_, err = l.Output(id)
	assert.Error(t, err, "a released id must be unknown afterward")

	// Releasing twice, or an id that never existed, is a benign no-op.
	assert.NoError(t, l.Release(context.Background(), id))
	assert.NoError(t, l.Release(context.Background(), "no-such-id"))
}

// TestTerminals_Release_AlsoKillsAnAlreadyFinishedTerminal: a terminal that
// already exited on its own still has a live tmux window behind it
// (remain-on-exit keeps the dead pane around) — release must kill it too,
// not only a still-running one, or the window leaks in the tmux session for
// the life of the server. Measured driving this end to end against real
// tmux: a released-but-never-killed window survived in `tmux ... list-windows`
// after the whole chat had ended.
func TestTerminals_Release_AlsoKillsAnAlreadyFinishedTerminal(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	id, err := l.Create(context.Background(), Spec{Command: "echo", Args: []string{"hi"}})
	require.NoError(t, err)

	term, ok := l.lookup(id)
	require.True(t, ok)
	require.NoError(t, os.WriteFile(term.statusPath, []byte("0\n"), 0o600))
	// Establish that the terminal is known to be FINISHED before releasing.
	_, err = l.Wait(context.Background(), id)
	require.NoError(t, err)

	require.NoError(t, l.Release(context.Background(), id))
	assert.True(t, f.calledWith("kill-window"), "release of an already-finished terminal must still kill its window")
}

// TestTerminals_UnknownId_Errors covers Output/Wait/Kill against an id that was
// never created.
func TestTerminals_UnknownId_Errors(t *testing.T) {
	f := newFakeTmuxRunner()
	l := New(f, t.TempDir())
	_, err := l.Output("nope")
	assert.Error(t, err)
	_, err = l.Wait(context.Background(), "nope")
	assert.Error(t, err)
	assert.Error(t, l.Kill(context.Background(), "nope"))
}

// TestTerminals_Create_TmuxMissing_FailsLoud: with tmux unreachable (the "tmux
// is not installed" case), Create returns the runner's error rather than
// falling back to any decline or no-op terminal. Turning that into a
// remedy-carrying message is the CALLER's job; this test pins that the failure
// actually PROPAGATES this far rather than being swallowed here.
func TestTerminals_Create_TmuxMissing_FailsLoud(t *testing.T) {
	f := newFakeTmuxRunner()
	f.failAll = errors.New(`exec: "tmux": executable file not found in $PATH`)
	l := New(f, t.TempDir())

	_, err := l.Create(context.Background(), Spec{Command: "echo"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "executable file not found")
}

// TestTerminals_EnsureSession_ToleratesConcurrentCreateRace: a new-session
// failure (another process won the race to create the same fixed session
// first) is tolerated as long as a re-checked has-session confirms the session
// now exists — never surfaced as a Create failure.
func TestTerminals_EnsureSession_ToleratesConcurrentCreateRace(t *testing.T) {
	r := &sequencedRunner{steps: []stepResult{
		{args0: "has-session", err: errors.New("no such session")},   // first probe: missing
		{args0: "new-session", err: errors.New("duplicate session")}, // lost the race to create it
		{args0: "has-session", err: nil},                             // re-check: it exists now
	}}
	l := New(r, t.TempDir())
	assert.NoError(t, l.ensureSession(context.Background()))
}

// TestTerminals_EnsureSession_SurfacesGenuineFailure: when the retry ALSO
// fails, ensureSession returns the original creation error rather than
// pretending the session exists.
func TestTerminals_EnsureSession_SurfacesGenuineFailure(t *testing.T) {
	r := &sequencedRunner{steps: []stepResult{
		{args0: "has-session", err: errors.New("no such session")},
		{args0: "new-session", err: errors.New("permission denied")},
		{args0: "has-session", err: errors.New("no such session")},
	}}
	l := New(r, t.TempDir())
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
