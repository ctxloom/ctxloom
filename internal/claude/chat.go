package claude

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// claudeACPAdapter is the ACP adapter binary that wraps claude for structured
// chat (Zed's adapter; claude-code has no native ACP mode). Located on PATH;
// ctxloom never installs binaries — an absent adapter yields the install hint
// below.
const claudeACPAdapter = "claude-code-acp"

// Compile-time assertion that ClaudeCode offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*ClaudeCode)(nil)

// Chat implements structured chat by delegating to the generic ACP driver over
// the claude-code-acp ADAPTER subprocess. This REPLACED the bespoke stream-json
// driver (chat_run.go/chat_stream.go, deleted once this path was verified live):
// one protocol mapper in internal/acp now serves every structured backend, and
// ACP's agent_thought_chunk surfaces summarized thinking that stream-json
// stripped. Materialization stays with this backend (Setup wrote .claude/ +
// .mcp.json + the framed context file; the adapter runs claude in the same cwd,
// which reads them natively); the driver only speaks the protocol.
//
// The interactive terminal path (a real `claude` TUI over the pty launcher) is
// untouched — ACP replaces only the programmatic conversation surface.
func (b *ClaudeCode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	if _, err := exec.LookPath(claudeACPAdapter); err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return fmt.Errorf("structured chat for claude needs the %s adapter on PATH; install it with: npm install -g @zed-industries/claude-code-acp", claudeACPAdapter)
	}
	drv := acp.NewChatDriver(chatACPConfig(b.Env))
	return drv.Chat(ctx, req, in, out)
}

// chatACPConfig is the adapter config for one claude structured-chat spawn.
//
// It strips claude's nested-session guard variable (CLAUDECODE) from the
// adapter's inherited environment. The agentcoord delegation topology spawns
// a child's whole engine chain from inside the PARENT claude's process tree
// (parent claude → its stdio `ctxloom mcp` hosting the coordinator → plugin →
// adapter → child claude), so the variable is inherited as pure lineage — but
// claude 2.x refuses to start when it is set ("Claude Code cannot be launched
// inside another Claude Code session"), killing every claude→claude
// delegation at session/new with an opaque -32603. This chat path only ever
// launches a DELIBERATE, independent engine driven over ACP in its own
// session — not a nested interactive session — and the guard's own message
// names unsetting the variable as the sanctioned bypass. The interactive pty
// path keeps the guard: there, nesting is real and the user's call.
func chatACPConfig(env map[string]string) acp.ACPConfig {
	return acp.ACPConfig{
		Command:  claudeACPAdapter,
		Env:      env,
		StripEnv: []string{"CLAUDECODE"},
		// The requested model must ALSO ride the adapter env as the claude
		// SDK's own ANTHROPIC_MODEL: claude-code-acp 0.16.2 silently ignores
		// the driver's `--model` argv (verified live), and without the env
		// var every session falls back to the user's saved INTERACTIVE
		// default from ~/.claude/settings.json — a saved alias (e.g.
		// "fable") then dies at the first session/prompt with the opaque
		// -32603 the resolveChatModel gate exists to prevent.
		ModelEnvVar: "ANTHROPIC_MODEL",
	}
}

// claudeModelNicknames translates claude's documented INTERACTIVE model
// nicknames — set via the TUI's `/model` picker and persisted as the user's
// saved default — to the concrete, API-shaped model id the ACP/API path
// actually accepts. A delegated child's whole engine chain is driven over ACP
// (claude-code-acp), never the interactive TUI: a bare nickname reaching its
// `--model` flag (or an unset model, which lets the adapter fall back to the
// saved interactive default) is REJECTED at session/new with an opaque
// -32603 ("There's an issue with the selected model") — the ACP/API surface,
// unlike the interactive TUI, has no alias table of its own. This is the ONE
// place that table lives; ResolveModel is the funnel every delegated child's
// model resolves through (see internal/operations/delegate.go's
// resolveChatModel, the upstream caller that actually gates the spawn).
var claudeModelNicknames = map[string]string{
	"fable":  "claude-fable-5",
	"opus":   "claude-opus-4-8",
	"sonnet": "claude-sonnet-5",
	"haiku":  "claude-haiku-4-5",
}

// ResolveModel translates a configured/raw claude model string into the
// concrete, ACP/API-shaped model id a delegated child's Chat spawn requires.
//
//   - A known interactive nickname (claudeModelNicknames) resolves to its
//     concrete id.
//   - A string that already LOOKS concrete (a "claude-" id carrying a version
//     digit, e.g. "claude-sonnet-5" or a dated snapshot) passes through
//     UNTOUCHED — a pinned concrete model is never rewritten.
//   - Anything else — empty, or an unrecognized bare word that looks like
//     another interactive-only alias ctxloom doesn't know how to translate —
//     reports ok=false so the caller can fail loud instead of spawning a
//     child the engine will reject with an opaque protocol error.
func ResolveModel(raw string) (model string, ok bool) {
	if raw == "" {
		return "", false
	}
	if concrete, known := claudeModelNicknames[strings.ToLower(raw)]; known {
		return concrete, true
	}
	if looksConcreteModel(raw) {
		return raw, true
	}
	return "", false
}

// looksConcreteModel is a permissive shape check distinguishing an
// already-resolved claude model id from a bare alias word no translation
// table recognizes: it must carry claude's "claude-" prefix AND at least one
// version digit (a bare word like a stray interactive nickname has neither).
func looksConcreteModel(s string) bool {
	if !strings.HasPrefix(s, "claude-") {
		return false
	}
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}
