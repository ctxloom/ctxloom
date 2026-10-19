//go:build !windows

package cli

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// lockedTranscript is everything read off a pty master, safe to read while
// the copy is still writing.
type lockedTranscript struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedTranscript) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedTranscript) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func (l *lockedTranscript) waitFor(t *testing.T, want string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for !strings.Contains(l.String(), want) {
		select {
		case <-deadline:
			t.Fatalf("the terminal never showed %q; it showed:\n%q", want, l.String())
		case <-time.After(time.Millisecond):
		}
	}
}

// The runner's own diagnostics never reach the terminal the engine draws on
// while the terminal UI owns the human's terminal. The engine runs on the
// runner's stdio, in the modes it set, so a warning printed to the runner's
// stderr lands in the middle of its screen. With the session's log named —
// the env the originator sets under the terminal UI (runnerTerminalEnv) — a
// warning the runner raises while the engine runs goes to the log. Without
// it, the same warning shows on the engine's terminal: the control that
// proves this test can see one there.
func TestRunnerDiagnostics_NeverReachTheEnginesTerminalUnderTheTerminalUI(t *testing.T) {
	for _, tc := range []struct {
		name     string
		diverted bool
	}{
		{"under the terminal UI", true},
		{"with no log named", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := pty.New()
			require.NoError(t, err)
			up, ok := p.(pty.UnixPty)
			require.True(t, ok)
			out := &lockedTranscript{}
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				_, _ = io.Copy(out, up.Master())
			}()
			t.Cleanup(func() {
				_ = p.Close()
				select {
				case <-drained:
				case <-time.After(10 * time.Second):
					t.Errorf("the terminal never reached its end after the launch: something still holds its slave")
				}
			})
			require.NoError(t, p.Resize(80, 24))
			slave := up.Slave()

			// The runner's stderr IS the terminal (runnerCmd binds os.Stderr
			// to it); the stand-in is the warning sink beneath any diversion.
			restoreTTY := clidiag.SetSink(slave)
			t.Cleanup(restoreTTY)
			dir := t.TempDir()
			log := filepath.Join(dir, "diagnostics.log")
			env := map[string]string{}
			if tc.diverted {
				env[sessions.EnvDiagnosticsLog] = log
			}
			restoreDiag := divertRunnerDiagnostics(func(k string) string { return env[k] }, func(k string) error { delete(env, k); return nil })
			t.Cleanup(restoreDiag)

			release := filepath.Join(dir, "release")
			done := make(chan struct{})
			go func() {
				defer close(done)
				_, _ = runner.RunLaunchSpec(context.Background(), agent.LaunchSpec{
					BinaryPath:  "/bin/sh",
					Args:        []string{"-c", `echo ENGINE-UP; while [ ! -e "$RELEASE" ]; do sleep 0.01; done; echo ENGINE-DONE`},
					Env:         append(os.Environ(), "RELEASE="+release),
					Interactive: true,
					Terminal:    &agent.Terminal{Stdin: slave, Stdout: slave, Stderr: slave},
				}, nil, io.Discard, io.Discard)
			}()
			out.waitFor(t, "ENGINE-UP")
			warning := "runner-side-warning-" + strings.ReplaceAll(tc.name, " ", "-")
			clidiag.Warn("ctxloom", "%s", warning)
			// The engine writes ENGINE-DONE only after the warning was raised,
			// so once it shows, a warning written to the terminal shows ahead
			// of it.
			require.NoError(t, os.WriteFile(release, nil, 0o600))
			out.waitFor(t, "ENGINE-DONE")
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("the launch never returned")
			}

			if !tc.diverted {
				assert.Contains(t, out.String(), warning, "with no log named the warning is on the engine's terminal")
				return
			}
			assert.NotContains(t, out.String(), warning, "the runner's warning must not land on the engine's terminal")
			got, err := os.ReadFile(log)
			require.NoError(t, err)
			assert.Contains(t, string(got), warning, "it lands in the session's diagnostics log")
		})
	}
}
