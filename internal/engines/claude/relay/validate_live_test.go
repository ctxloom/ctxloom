package relay_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aymanbagabas/go-pty"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner/interaction"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/spool"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// The live wake gate (`just validate-wake-claude`), REQUIRED on every claude
// pin bump: claude's cross-session messaging is undocumented and can change
// under a version without notice, and the alarm only notices afterwards.
//
// It runs the REAL pieces end to end against the INSTALLED claude, in a
// throwaway HOME and CLAUDE_CONFIG_DIR:
//   - a real session endpoint (runner/interaction) and its WakeSignal;
//   - the ctxloom entry claude's own definition renders — `ctxloom
//     claude-relay`, resolved on PATH to the binary under test;
//   - the real turn-start hook, `ctxloom hook mail-drain`, beside a hook that
//     records the payload.
//
// It asserts the three facts the design stands on:
//  1. claude exports its messaging endpoint to a stdio MCP child — the relay
//     subscribes to the wake only once that bind succeeded;
//  2. a post from that child starts a turn in an IDLE interactive session —
//     the turn-start hooks run;
//  3. UserPromptSubmit's prompt is the BARE wake line, and mail-drain
//     redeems its nonce;
//  4. claude kept the endpoint's server instructions WHOLE — it truncates
//     past a cap (operations.InstructionsCharCap) and logs that it did.
//
// No model call is made: the spool holds no mail, so mail-drain blocks the
// woken prompt as a stale wake. Credentials come from the caller's
// environment and are never printed.
const (
	envValidateCtxloom = "VALIDATE_WAKE_CLAUDE_CTXLOOM"
	envValidateClaude  = "VALIDATE_WAKE_CLAUDE_BIN"
	liveBound          = 60 * time.Second
	liveHarp           = "wake-probe-cell"
)

func TestValidateWakeClaude(t *testing.T) {
	ctxloomBin, claudeBin := os.Getenv(envValidateCtxloom), os.Getenv(envValidateClaude)
	if ctxloomBin == "" || claudeBin == "" {
		t.Skipf("live: set %s and %s (just validate-wake-claude)", envValidateCtxloom, envValidateClaude)
	}
	version, err := exec.Command(claudeBin, "--version").Output()
	require.NoError(t, err)
	cell := "claude=" + strings.Fields(string(version))[0] + "/host/bypass/interactive"
	t.Run(cell, func(t *testing.T) { validateWake(t, ctxloomBin, claudeBin) })
}

func validateWake(t *testing.T, ctxloomBin, claudeBin string) {
	root, err := os.MkdirTemp("", "vwc")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	home, cfg, work := filepath.Join(root, "h"), filepath.Join(root, "c"), filepath.Join(root, "w")
	for _, d := range []string{home, cfg, work} {
		require.NoError(t, os.MkdirAll(d, 0o700))
	}

	// The spool the hook reads is the throwaway HOME's.
	t.Setenv("HOME", home)
	mapper := spool.NewHomeMapper()
	nonce, err := spool.ArmWake(mapper, liveHarp)
	require.NoError(t, err)

	url, sig := endpoint(t)
	e, err := claude.Build()
	require.NoError(t, err)
	entry := e.Root().Dynamic.Endpoint(sessions.Endpoint{URL: url, Credential: bearer})
	mcpFile := filepath.Join(root, "mcp.json")
	writeJSON(t, mcpFile, map[string]any{"mcpServers": map[string]any{"ctxloom": map[string]any{
		"type": "stdio", "command": entry.Command, "args": entry.Args, "env": entry.Env,
	}}})

	captured := filepath.Join(root, "hook-in.jsonl")
	capture := filepath.Join(root, "capture.sh")
	require.NoError(t, os.WriteFile(capture, []byte("#!/bin/sh\ncat >> '"+captured+"'\necho >> '"+captured+"'\n"), 0o700))
	writeJSON(t, filepath.Join(cfg, "settings.json"), map[string]any{
		"env":                               map[string]string{"DISABLE_AUTOUPDATER": "1"},
		"skipDangerousModePermissionPrompt": true,
		"hooks": map[string]any{claude.HookEventUserPromptSubmit: []any{map[string]any{"hooks": []any{
			map[string]any{"type": "command", "command": capture},
			map[string]any{"type": "command", "command": agent.NewMailDrainHook().Command},
		}}}},
	})
	writeJSON(t, filepath.Join(cfg, ".claude.json"), map[string]any{
		"hasCompletedOnboarding": true, "theme": "dark", "bypassPermissionsModeAccepted": true,
		"projects": map[string]any{work: map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}},
	})

	tty, err := pty.New()
	require.NoError(t, err)
	// Sized before the start, so claude's first paint is at this geometry.
	require.NoError(t, tty.Resize(160, 50))
	cmd := tty.Command(claudeBin, "--model", "haiku", "--dangerously-skip-permissions", "--debug", "--mcp-config", mcpFile, "--strict-mcp-config")
	cmd.Dir = work
	cmd.Env = liveEnv(home, cfg, filepath.Dir(ctxloomBin))
	require.NoError(t, cmd.Start())
	screen := drain(tty)
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = tty.Close()
		t.Logf("screen tail:\n%s", screen.tail(1500))
		logMessagingDebug(t, cfg)
	})

	// (1) The relay subscribes only once it has bound claude's wake from what
	// claude exported to it: a successful Fire IS the proof of the export.
	fireWhenSubscribed(t, sig, nonce)
	t.Logf("fact 1: the relay claude spawned bound its wake and is subscribed; wake %s fired", nonce)

	// (2)+(3) The turn-start hooks ran with the bare line, and mail-drain
	// redeemed the nonce.
	prompt := awaitPrompt(t, captured, nonce)
	require.Equal(t, engine.WakeText(nonce), prompt, "UserPromptSubmit's prompt is the bare wake line")
	t.Logf("fact 2+3: a turn started; UserPromptSubmit's prompt is exactly %q", prompt)
	deadline := time.Now().Add(liveBound)
	for {
		out, err := spool.OutstandingWake(mapper, liveHarp)
		require.NoError(t, err)
		if len(out) == 0 {
			break
		}
		require.True(t, time.Now().Before(deadline), "mail-drain never redeemed wake %s", nonce)
		time.Sleep(200 * time.Millisecond)
	}
	t.Logf("fact 3: mail-drain redeemed wake %s", nonce)

	// (4) Not fatal: facts 1-3 stand on their own and are already logged. The
	// absence of a truncation line means something only if claude logged the
	// ctxloom connection it would have been logged beside.
	connected, truncated := false, false
	for _, line := range debugLines(cfg) {
		connected = connected || ctxloomConnected.MatchString(line)
		if m := truncatedInstructions.FindStringSubmatch(line); m != nil {
			truncated = true
			t.Errorf("fact 4: claude truncated the server instructions from %s to %s chars (operations.InstructionsCharCap is %d): the tail never reaches the agent",
				m[1], m[2], operations.InstructionsCharCap)
		}
	}
	switch {
	case !connected:
		t.Errorf("fact 4: claude's debug log never recorded the ctxloom connection, so the absence of a truncation line proves nothing")
	case !truncated:
		t.Logf("fact 4: claude kept the server instructions whole")
	}
}

// ctxloomConnected is claude's --debug line for the relay's MCP connection.
var ctxloomConnected = regexp.MustCompile(`MCP server "ctxloom": Successfully connected`)

// truncatedInstructions is claude's --debug line for server instructions it
// cut to its cap; on 2.1.286: `MCP server "ctxloom": Server instructions
// truncated from 2659 to 2048 chars`.
var truncatedInstructions = regexp.MustCompile(`Server instructions truncated from (\d+) to (\d+) chars`)

// inherited names what must not reach the probe from the caller: the marks
// of a parent claude session (a probe run from inside claude would otherwise
// be a child session, with that session's messaging endpoint), an IDE's
// (which puts a modal welcome over the prompt), and a ctxloom session's.
var inherited = regexp.MustCompile(`^(CLAUDECODE|CLAUDE_PID|CLAUDE_CONFIG_DIR|CLAUDE_CODE_(MESSAGING_.*|CHILD_SESSION|ENTRYPOINT|EXECPATH|SESSION_.*|SSE_PORT)|CTXLOOM_.*|VSCODE_.*|TERM_PROGRAM.*|GIT_ASKPASS|HOME|PATH|TERM)$`)

// liveEnv is the caller's environment (its credentials included, never
// printed) in the throwaway HOME and config dir, with the binary under test
// first on PATH.
func liveEnv(home, cfg, binDir string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); !inherited.MatchString(k) {
			env = append(env, kv)
		}
	}
	return append(env, "HOME="+home, "CLAUDE_CONFIG_DIR="+cfg, "TERM=xterm-256color",
		"PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), sessions.EnvHarp+"="+liveHarp)
}

func fireWhenSubscribed(t *testing.T, sig *interaction.WakeSignal, nonce string) {
	t.Helper()
	deadline := time.Now().Add(liveBound)
	for {
		err := sig.Fire(context.Background(), nonce)
		if !errors.Is(err, interaction.ErrNoWakeSubscriber) {
			require.NoError(t, err)
			return
		}
		require.True(t, time.Now().Before(deadline), "claude's relay never subscribed to the wake: claude did not spawn it, or it could not bind claude's messaging endpoint (see the screen tail)")
		time.Sleep(250 * time.Millisecond)
	}
}

func awaitPrompt(t *testing.T, captured, nonce string) string {
	t.Helper()
	deadline := time.Now().Add(liveBound)
	for {
		raw, _ := os.ReadFile(captured)
		for _, line := range strings.Split(string(raw), "\n") {
			var p struct {
				Event  string `json:"hook_event_name"`
				Prompt string `json:"prompt"`
			}
			if json.Unmarshal([]byte(line), &p) == nil && strings.Contains(p.Prompt, nonce) {
				return p.Prompt
			}
		}
		require.True(t, time.Now().Before(deadline), "no turn started on wake %s: UserPromptSubmit never saw it", nonce)
		time.Sleep(200 * time.Millisecond)
	}
}

// logMessagingDebug logs claude's own debug lines about cross-session
// messaging and MCP for the relay — the evidence of what claude did with the
// post — with every long hex run (session keys, tokens) redacted.
func logMessagingDebug(t *testing.T, cfg string) {
	t.Helper()
	relevant := regexp.MustCompile(`(?i)uds|messaging|cross-session|peer|inbox|mcp.*ctxloom|UserPromptSubmit`)
	hex := regexp.MustCompile(`[0-9a-fA-F]{24,}`)
	for _, line := range debugLines(cfg) {
		if relevant.MatchString(line) {
			if len(line) > 300 {
				line = line[:300]
			}
			t.Logf("claude debug: %s", hex.ReplaceAllString(line, "<hex>"))
		}
	}
}

// debugLines is every line of claude's --debug logs under its config dir.
func debugLines(cfg string) []string {
	files, _ := filepath.Glob(filepath.Join(cfg, "debug", "*"))
	var lines []string
	for _, f := range files {
		if info, err := os.Lstat(f); err != nil || !info.Mode().IsRegular() {
			continue
		}
		raw, _ := os.ReadFile(f)
		lines = append(lines, strings.Split(string(raw), "\n")...)
	}
	return lines
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.MarshalIndent(v, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, raw, 0o600))
}

// screenBuf keeps what claude draws, so a failure can show where it stopped.
type screenBuf struct {
	mu  sync.Mutex
	buf []byte
}

var ansi = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[\]P][^\x07\x1b]*(\x07|\x1b\\)|\x1b[()][0-9A-B]`)

func (s *screenBuf) tail(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain := ansi.ReplaceAllString(string(s.buf), "")
	if len(plain) > n {
		plain = plain[len(plain)-n:]
	}
	return plain
}

func drain(r io.Reader) *screenBuf {
	s := &screenBuf{}
	go func() {
		chunk := make([]byte, 32<<10)
		for {
			n, err := r.Read(chunk)
			s.mu.Lock()
			s.buf = append(s.buf, chunk[:n]...)
			s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	return s
}
