//go:build integration

package integration

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/claude"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// TestACPLive_ClaudeCodeAdapter drives the REAL claude-code-acp adapter (which
// spawns a real claude) through the generic ACP driver: initialize →
// session/new → one session/prompt turn, asserting assistant text streams back
// as mapped entries and the turn completes. This is the Phase-0 live spike from
// the ACP plan, kept as a lasting gate for the driver's guessed wire fields
// (clientInfo, ndjson framing, permission shape).
//
// Self-skips without the adapter on PATH (npm i -g @zed-industries/claude-code-acp)
// or without ambient claude auth (~/.claude/.credentials.json or ANTHROPIC_API_KEY),
// so the hermetic suite never depends on it.
func TestACPLive_ClaudeCodeAdapter(t *testing.T) {
	if _, err := exec.LookPath("claude-code-acp"); err != nil {
		t.Skip("claude-code-acp not on PATH; install with: npm install -g @zed-industries/claude-code-acp")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home dir to locate claude credentials")
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
			t.Skip("no ambient claude auth (ANTHROPIC_API_KEY or ~/.claude credentials)")
		}
	}

	// When this suite itself runs inside a Claude Code session, the inherited
	// CLAUDECODE var makes the adapter's nested claude refuse to launch. That is
	// claude's own nesting guard — the DRIVER deliberately does not override it,
	// so the test scrubs its own process env instead.
	if _, ok := os.LookupEnv("CLAUDECODE"); ok {
		// t.Setenv captures the CURRENT value and restores it at test end; the
		// unset that follows is what the adapter actually needs, and the
		// restore it registered still puts the original back.
		t.Setenv("CLAUDECODE", "")
		require.NoError(t, os.Unsetenv("CLAUDECODE"))
	}

	drv := acp.NewChatDriver(acp.ACPConfig{Command: "claude-code-acp"})

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 1)
	in <- agent.ChatMessage{Text: "Reply with exactly the single word: pong"}
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- drv.Chat(ctx, agent.ChatRequest{
			WorkDir: t.TempDir(),
			// Pin a universally-available model: the adapter's nested claude
			// otherwise inherits the host's default, which may be unavailable to
			// the adapter's API path (observed live with a session-only model).
			Env: map[string]string{"ANTHROPIC_MODEL": "sonnet"},
			// Permissions left at the zero value (PermissionDefault): no
			// auto-approve, matching the old AutoApprove: false intent.
		}, in, out)
	}()

	var text strings.Builder
	var completes int
	for ev := range out {
		switch {
		case ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant:
			text.WriteString(ev.Entry.Content)
		case ev.Complete != nil:
			completes++
		}
	}
	require.NoError(t, <-done, "the live ACP conversation must complete cleanly")
	assert.Contains(t, strings.ToLower(text.String()), "pong",
		"assistant text must stream back through the ACP mapping")
	assert.Equal(t, 1, completes, "exactly one turn-completion marker")
}

// TestACPLive_ClaudeBackendDelegation drives the claude BACKEND's Chat (the
// delegation that replaced the bespoke stream-json driver), not the raw ACP
// driver — the exact seam the gRPC chat bridge type-asserts. Same skips as
// above.
func TestACPLive_ClaudeBackendDelegation(t *testing.T) {
	if _, err := exec.LookPath("claude-code-acp"); err != nil {
		t.Skip("claude-code-acp not on PATH; install with: npm install -g @zed-industries/claude-code-acp")
	}
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skip("no home dir to locate claude credentials")
		}
		if _, err := os.Stat(filepath.Join(home, ".claude", ".credentials.json")); err != nil {
			t.Skip("no ambient claude auth (ANTHROPIC_API_KEY or ~/.claude credentials)")
		}
	}
	if _, ok := os.LookupEnv("CLAUDECODE"); ok {
		// t.Setenv captures the CURRENT value and restores it at test end; the
		// unset that follows is what the adapter actually needs, and the
		// restore it registered still puts the original back.
		t.Setenv("CLAUDECODE", "")
		require.NoError(t, os.Unsetenv("CLAUDECODE"))
	}

	backend := claude.NewClaudeCode()
	var chat agent.StructuredChat = backend // the bridge's own type assertion

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	in := make(chan agent.ChatMessage, 1)
	in <- agent.ChatMessage{Text: "Reply with exactly the single word: ping"}
	close(in)
	out := make(chan agent.ChatEvent, 64)

	done := make(chan error, 1)
	go func() {
		done <- chat.Chat(ctx, agent.ChatRequest{
			WorkDir: t.TempDir(),
			Env:     map[string]string{"ANTHROPIC_MODEL": "sonnet"},
		}, in, out)
	}()

	var text strings.Builder
	for ev := range out {
		if ev.Entry != nil && ev.Entry.Type == agent.EntryTypeAssistant {
			text.WriteString(ev.Entry.Content)
		}
	}
	require.NoError(t, <-done)
	assert.Contains(t, strings.ToLower(text.String()), "ping")
}
