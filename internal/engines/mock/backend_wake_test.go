package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// The mock is the session owner acceptance drives, on the runner's own
// terminal: its interactive echo is a TUI a human types at. A wake is a
// line posted to the mock's own socket, which its exec names
// (EnvWakeSocket), and the echo takes it exactly as a typed line — the
// turn_start hooks fire with it as the prompt, and it is echoed.

func ownerSession(harp, workDir string) engine.Session {
	return engine.Session{Identity: sessions.Identity{Harp: harp}, Mode: engine.Interactive, WorkDir: workDir}
}

// TestExec_AnInteractiveSessionNamesItsWakeSocket: the exec names the socket
// the mock's interactive echo listens on, and the runner binds the mock's
// wake from that same exec env. A structured turn has nobody to wake.
func TestExec_AnInteractiveSessionNamesItsWakeSocket(t *testing.T) {
	m := New().(Mock)
	inst, err := m.Instance(ownerSession("owner-harp", t.TempDir()))
	require.NoError(t, err)
	ex, err := inst.Exec(nil)
	require.NoError(t, err)
	path := ex.Env[EnvWakeSocket]
	require.NotEmpty(t, path)
	assert.True(t, filepath.IsAbs(path))
	assert.Contains(t, path, "owner-harp", "one socket per session")
	assert.LessOrEqual(t, len(path), maxUnixSocketPath, "a unix socket path longer than the kernel's limit cannot be listened on")

	s := ownerSession("child-harp", t.TempDir())
	s.Mode = engine.Structured
	inst, err = m.Instance(s)
	require.NoError(t, err)
	ex, err = inst.Exec(nil)
	require.NoError(t, err)
	assert.NotContains(t, ex.Env, EnvWakeSocket)
}

// hookRecorder writes a hooks file whose turn_start hook appends its stdin
// payload, one JSON object per line, to a file the test reads back.
func hookRecorder(t *testing.T) (presented []present.Presentation, payloads func() []hookPayload) {
	t.Helper()
	dir := t.TempDir()
	out := filepath.Join(dir, "payloads.jsonl")
	hooks := wire.UnifiedHooks{TurnStart: []wire.Hook{{Type: "command", Command: "cat >> '" + out + "'; echo >> '" + out + "'"}}}
	raw, err := json.Marshal(hooks)
	require.NoError(t, err)
	file := filepath.Join(dir, "hooks.json")
	require.NoError(t, os.WriteFile(file, raw, 0o600))
	return []present.Presentation{{Args: []string{HooksFlag, file}}}, func() []hookPayload {
		raw, err := os.ReadFile(out)
		if os.IsNotExist(err) {
			return nil
		}
		require.NoError(t, err)
		var got []hookPayload
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var p hookPayload
			require.NoError(t, json.Unmarshal([]byte(line), &p))
			got = append(got, p)
		}
		return got
	}
}

// syncWriter is a strings.Builder safe to read while the echo writes; every
// write closes and replaces changed, so a waiter blocks on the next write
// instead of polling.
type syncWriter struct {
	mu      sync.Mutex
	b       strings.Builder
	changed chan struct{}
}

func newSyncWriter() *syncWriter { return &syncWriter{changed: make(chan struct{})} }

func (w *syncWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.b.Write(p)
	close(w.changed)
	w.changed = make(chan struct{})
	return n, err
}

func (w *syncWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.b.String()
}

// waitFor blocks until the output contains want, failing after d.
func (w *syncWriter) waitFor(t *testing.T, d time.Duration, want string) {
	t.Helper()
	deadline := time.After(d)
	for {
		w.mu.Lock()
		has, changed := strings.Contains(w.b.String(), want), w.changed
		w.mu.Unlock()
		if has {
			return
		}
		select {
		case <-changed:
		case <-deadline:
			t.Fatalf("the output never showed %q:\n%s", want, w.String())
		}
	}
}

// TestInteractiveEcho_ATypedLineFiresTurnStartWithItsPrompt: every non-blank
// typed line is a prompt submitted, so the session's turn_start hooks fire
// with it — the hook the session owner's mail rides.
// MUTATION — fire turn_start without the line as the prompt — turns this red.
func TestInteractiveEcho_ATypedLineFiresTurnStartWithItsPrompt(t *testing.T) {
	presented, payloads := hookRecorder(t)
	s := ownerSession("typed-harp", t.TempDir())
	req := &agent.ExecuteRequest{
		Env:       map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Stdin:     strings.NewReader("what did the child say?\nquit\n"),
		Session:   &s,
		Presented: presented,
		WorkDir:   s.WorkDir,
	}
	out := newSyncWriter()
	_, err := newTestBackend().Execute(context.Background(), req, out, out)
	require.NoError(t, err)
	assert.Contains(t, out.String(), "mock echo: what did the child say?")
	got := payloads()
	require.Len(t, got, 1, "one prompt, one turn_start; quit is not a prompt")
	assert.Equal(t, "turn_start", got[0].Event)
	assert.Equal(t, "what did the child say?", got[0].Prompt)
}

// TestInteractiveEcho_AWakeIsTakenAsATypedLine: a wake bound the way the
// runner binds it — the mock's declared spec over the exec env — posts to the
// socket the echo listens on; the line is echoed and fires turn_start with
// the wake text as its prompt.
// MUTATION — do not listen on the exec's wake socket — turns this red.
func TestInteractiveEcho_AWakeIsTakenAsATypedLine(t *testing.T) {
	presented, payloads := hookRecorder(t)
	s := ownerSession(fmt.Sprintf("wake-%d", time.Now().UnixNano()%1_000_000_000), t.TempDir())
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })
	req := &agent.ExecuteRequest{
		Env:       map[string]string{"CTXLOOM_MOCK_ECHO_STDIN": "1"},
		Stdin:     pr,
		Session:   &s,
		Presented: presented,
		WorkDir:   s.WorkDir,
	}
	out := newSyncWriter()
	done := make(chan error, 1)
	go func() {
		_, err := newTestBackend().Execute(context.Background(), req, out, out)
		done <- err
	}()
	// The echo is standing once a typed line comes back: the socket is
	// listened on before the first line is read.
	_, err := pw.Write([]byte("ready\n"))
	require.NoError(t, err)
	out.waitFor(t, 5*time.Second, "mock echo: ready")

	inst, err := New().(Mock).Instance(s)
	require.NoError(t, err)
	ex, err := inst.Exec(presented)
	require.NoError(t, err)
	spec, ok := New().Wake().Get()
	require.True(t, ok)
	w, err := spec.Bind(context.Background(), func(k string) (string, bool) { v, ok := ex.Env[k]; return v, ok })
	require.NoError(t, err)
	require.NoError(t, w.Fire(context.Background(), "0123456789abcdef"))

	wake := engine.WakeText("0123456789abcdef")
	out.waitFor(t, 5*time.Second, "mock echo: "+wake)
	_, err = pw.Write([]byte("quit\n"))
	require.NoError(t, err)
	require.NoError(t, testsupport.Await(t, 5*time.Second, done, "the echo did not end on quit"))

	got := payloads()
	require.Len(t, got, 2)
	assert.Equal(t, wake, got[1].Prompt, "the wake's turn_start carries the wake text, which mail-drain redeems")
	_, statErr := os.Stat(ex.Env[EnvWakeSocket])
	assert.True(t, os.IsNotExist(statErr), "the socket is removed when the session ends")
}
