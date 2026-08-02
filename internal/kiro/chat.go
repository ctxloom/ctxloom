package kiro

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
// Unlike codex-acp, `kiro-cli acp --model <id>` is a real, HONORED flag: a
// live authenticated session both self-reports the requested model and
// shifts wall-clock turn duration with it, and an unrecognized --model value
// surfaces as a genuine mid-session RPC error ("model '<x>' is not
// available") rather than being silently accepted (cf. claude-code-acp's
// known silent-ignore shape). No ModelConfigKey/ModelEnvVar override is
// wired: the generic driver's --model argv is kiro's own established
// mechanism (buildArgs in backend.go already uses it for the oneshot
// `kiro-cli chat` path too).
//
// It sets no ReasoningConfigKey/ReasoningEffort (acp.ACPConfig): the
// cross-engine normalized thinking level (off|low|medium|high — see
// internal/claude/chat.go, internal/codex/chat.go) is a DOCUMENTED no-op on
// this backend, not a silent swallow — KiroConfig.Thinking's Configure warns
// if a user sets it (backend.go). kiro-cli acp may well have its own
// reasoning knob (its direct-CLI --effort exists — buildArgs), but wiring
// the normalized enum onto it is unresearched; do not assume the two
// vocabularies line up without verifying kiro-cli acp's own flags live.
func (b *Kiro) chatACPConfig() acp.ACPConfig {
	// `effort:` IS delivered on the direct-CLI path (buildArgs emits
	// --effort) but has no verified counterpart on `kiro-cli acp`, so this
	// path cannot honor it. Say so: silently dropping a knob the user
	// explicitly set means the session runs at kiro's default effort while
	// every surface reports success. Passing --effort through on
	// speculation is the worse option — an unsupported flag would break the
	// spawn outright, and this file's doc comment already refuses to assume
	// the two vocabularies line up.
	if b.effort != "" {
		clidiag.Warn("ctxloom", "kiro config sets effort %q, but the structured-chat (`kiro-cli acp`) path has no verified effort mechanism — this session runs at kiro's own default; effort applies to the direct `kiro-cli chat` path only", b.effort)
	}
	return acp.ACPConfig{
		Command:     b.BinaryPath + " acp",
		Agent:       b.agentName,
		AgentEngine: b.agentEngine,
		Args:        b.Args,
		Env:         b.Env,
	}
}
