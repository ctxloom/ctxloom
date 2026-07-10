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
	drv := acp.NewChatDriver(chatACPConfig(b.Env))
	return drv.Chat(ctx, req, in, out)
}

// chatACPConfig is the adapter config for one codex structured-chat spawn.
// Unlike claude's (internal/claude/chat.go), it strips nothing: codex has no
// nested-session guard, so inherited engine-lineage variables are inert.
//
// It sets ModelConfigKey rather than relying on the driver's generic --model
// flag: codex-acp 0.16.0 has NO --model flag at all (verified live, Wave
// C3 — `codex-acp --model <x>` exits 2, "unexpected argument '--model'
// found", a hard spawn failure, not claude-code-acp's silent-ignore
// shape). Model selection rides codex-acp's own `-c key=value` config
// override instead (its config.toml dotted-path convention — README:
// `-c model="o3"`, also verified live), so ModelConfigKey names the dotted
// key "model" and the driver renders `-c model="<value>"`. --agent and
// --agent-engine are ALSO rejected the same way (verified live), which is
// why this config never sets Agent/AgentEngine.
func chatACPConfig(env map[string]string) acp.ACPConfig {
	return acp.ACPConfig{
		Command:        codexACPAdapter,
		Env:            env,
		ModelConfigKey: "model",
	}
}
