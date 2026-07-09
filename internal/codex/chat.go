package codex

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// codexACPAdapter is the ACP adapter binary that wraps codex (codex has no
// native ACP mode). Located on PATH; ctxloom never installs binaries — an
// absent adapter yields the install hint below.
const codexACPAdapter = "codex-acp"

// Compile-time assertion that Codex offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Codex)(nil)

// Chat implements structured chat by delegating to the generic ACP driver over
// the codex-acp ADAPTER subprocess (codex itself has no programmatic protocol —
// this is codex's first structured-chat path, additive to the interactive
// terminal). Materialization stays with this backend (Setup wrote codex's
// native config; the adapter runs codex in the same cwd); the driver only
// speaks the protocol.
func (b *Codex) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	if _, err := exec.LookPath(codexACPAdapter); err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return fmt.Errorf("structured chat for codex needs the %s adapter on PATH; install it with: npm install -g @zed-industries/codex-acp", codexACPAdapter)
	}
	drv := acp.NewChatDriver(acp.ACPConfig{
		Command: codexACPAdapter,
		Env:     b.Env,
	})
	return drv.Chat(ctx, req, in, out)
}
