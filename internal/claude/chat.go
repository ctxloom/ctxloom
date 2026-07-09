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
//
// Note: claude's own nested-session guard (the CLAUDECODE env var) applies to
// the adapter's spawned claude. The driver deliberately does NOT strip it —
// overriding an upstream safety mechanism is the user's call, not ctxloom's.
func (b *ClaudeCode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	if _, err := exec.LookPath(claudeACPAdapter); err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return fmt.Errorf("structured chat for claude needs the %s adapter on PATH; install it with: npm install -g @zed-industries/claude-code-acp", claudeACPAdapter)
	}
	drv := acp.NewChatDriver(acp.ACPConfig{
		Command: claudeACPAdapter,
		Env:     b.Env,
	})
	return drv.Chat(ctx, req, in, out)
}
