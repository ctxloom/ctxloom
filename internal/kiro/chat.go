package kiro

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// Compile-time assertion that Kiro offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Kiro)(nil)

// Chat implements structured chat by delegating to the generic ACP driver over
// `kiro-cli acp` — Kiro speaks ACP natively, so the direct-CLI backend needs no
// bespoke stream mapper. Materialization stays HERE (this backend's Setup wrote
// the .kiro/ config the spawned agent reads from cwd, including the ctxloom
// agent selected via --agent — chat is a full-setup path, unlike the SkipSetup
// oneshot fan-out); the driver only speaks the protocol.
func (b *Kiro) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	drv := acp.NewChatDriver(b.chatACPConfig())
	return drv.Chat(ctx, req, in, out)
}

// chatACPConfig is the adapter config for one kiro structured-chat spawn.
//
// Unlike codex-acp, `kiro-cli acp --model <id>` is a real, CLI-parse-accepted
// flag (verified live, Wave C3: `kiro-cli --help-all` documents it, and an
// unauthenticated live spawn with an arbitrary --model value fails on the
// auth check — "not logged in" — never on flag parsing, so the flag reaches
// argument validation cleanly). No ModelConfigKey/ModelEnvVar override is
// wired: the generic driver's --model argv is kiro's own established
// mechanism (buildArgs in backend.go already uses it for the oneshot
// `kiro-cli chat` path). RESIDUAL: whether kiro-cli acp actually HONORS
// --model (vs. claude-code-acp's known silent-ignore shape) could not be
// live-confirmed on this host — kiro-cli acp requires an authenticated
// session before it even opens its JSON-RPC loop (no stdout at all pre-auth,
// unlike codex-acp/claude-code-acp), so no live turn was possible; flagged
// for live verification once kiro-cli login is available.
func (b *Kiro) chatACPConfig() acp.ACPConfig {
	return acp.ACPConfig{
		Command:     b.BinaryPath + " acp",
		Agent:       b.agentName,
		AgentEngine: b.agentEngine,
		Args:        b.Args,
		Env:         b.Env,
	}
}
