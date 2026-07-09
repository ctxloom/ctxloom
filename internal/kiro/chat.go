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
//
// UNVERIFIED live (auth-gated), like the settings writer: the exact `kiro-cli
// acp` flag surface pends a real authenticated run.
func (b *Kiro) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	drv := acp.NewChatDriver(acp.ACPConfig{
		Command:     b.BinaryPath + " acp",
		Agent:       b.agentName,
		AgentEngine: b.agentEngine,
		Args:        b.Args,
		Env:         b.Env,
	})
	return drv.Chat(ctx, req, in, out)
}
