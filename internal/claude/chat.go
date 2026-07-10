package claude

import (
	"context"
	"fmt"
	"os/exec"

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
	}
}
