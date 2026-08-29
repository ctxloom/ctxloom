//go:build parked_engines

package codex

import (
	"context"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// CodexACPAdapter is the ACP adapter binary that wraps codex (codex has no
// native ACP mode). Located on PATH; ctxloom never installs binaries — an
// absent adapter yields the install hint Chat() reports below.
const CodexACPAdapter = "codex-acp"

// CodexACPTransport is this engine's ONE ACP-transport declaration. It lives
// in this package so NewCodex sets it on every instance (incl. direct
// construction) via SetACPTransport, closing the zero-value footgun. The
// registry's agentDescriptor reads codex.CodexACPTransport for
// DOCTOR-CHECK-ACPADAPTER-m3; the Chat() gate reads it off the injected
// instance. No import cycle: backends imports codex, never the reverse.
var CodexACPTransport = agent.ACPTransport{
	Kind:       agent.ACPAdapter,
	Binary:     CodexACPAdapter,
	InstallCmd: "npm install -g @zed-industries/codex-acp",
	Publisher:  "Zed Industries",
	SourceRepo: "https://github.com/zed-industries/codex-acp",
}

// Compile-time assertion that Codex offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*Codex)(nil)

// Chat implements structured chat by delegating to the generic ACP driver over
// the codex-acp ADAPTER subprocess (codex itself has no programmatic protocol —
// this is codex's first structured-chat path, additive to the interactive
// terminal). Materialization stays with this backend (Setup wrote codex's
// native config; the adapter runs codex in the same cwd); the driver only
// speaks the protocol.
func (b *Codex) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	// The host-PATH adapter check only applies to a HOST-runtime chat: a
	// runtime:container agent runs codex-acp INSIDE the agent image instead
	// (ISO1's container transport, internal/acp), where the image carries
	// the adapter — checking THIS process's PATH would wrongly refuse a
	// session whose adapter the host never needs.
	//
	// transport is this backend's ONE ACP-transport declaration (agent.
	// ACPTransport), injected at construction by internal/lm/backends'
	// registry (SetACPTransport) — the SAME value ACPTransportFor("codex")
	// reports to callers with no backend instance (doctor_cmd.go). This
	// package cannot import backends itself (backends already imports codex
	// to register it — that would cycle), so the value arrives by injection
	// instead of a cross-package accessor call. RequireOnHost is the shared
	// gate every ACPAdapter engine runs (agent.ACPTransport's doc).
	transport := b.ACPTransport()
	if err := transport.RequireOnHost(req.Runtime, "codex"); err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return err
	}
	drv := acp.NewChatDriver(chatACPConfig(b.Env, b.thinking, transport.Binary))
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
//
// ReasoningConfigKey/ReasoningEffort and ReasoningSummaryConfigKey/
// ReasoningSummary deliver the normalized thinking level the SAME way, via an
// ADDITIONAL `-c key=value` override independent of the model one — both
// render whenever set (internal/acp's chatArgv). Unlike Model there is no
// per-request override: level is resolved from the LABELED config's static
// `thinking` value (Configure, backend.go), so the concrete strings are
// computed once, here, not per turn.
//
// binary is the adapter command to spawn — Chat() passes its own
// b.ACPTransport().Binary rather than this function reading CodexACPAdapter
// directly, so every caller (including tests) says explicitly which
// transport declaration it's exercising.
func chatACPConfig(env map[string]string, level agent.ThinkingLevel, binary string) acp.ACPConfig {
	cfg := acp.ACPConfig{
		Command:            binary,
		Env:                env,
		ModelConfigKey:     "model",
		ReasoningConfigKey: "model_reasoning_effort",
		ReasoningEffort:    codexReasoningEffort(level),
	}
	if summary, ok := codexReasoningSummary(level); ok {
		cfg.ReasoningSummaryConfigKey = "model_reasoning_summary"
		cfg.ReasoningSummary = summary
	}
	return cfg
}

// codexReasoningEffort translates the normalized level into codex-acp's real
// model_reasoning_effort values, VERIFIED from the binary: minimal, low,
// medium, xhigh — there is NO bare "high". The normalized "high" therefore
// maps to codex's "xhigh", not a nonexistent "high" that would fail codex's
// own validation. "off" maps to codex's lowest tier ("minimal") rather than
// omitting the key: codex has no reasoning-disabled state of its own to fall
// back to, and the labeled config's declared level should still narrow
// effort as far as codex's vocabulary allows.
func codexReasoningEffort(level agent.ThinkingLevel) string {
	switch level {
	case agent.ThinkingOff:
		return "minimal"
	case agent.ThinkingLow:
		return "low"
	case agent.ThinkingHigh:
		return "xhigh" // codex has no bare "high" — verified live
	default: // ThinkingMedium, and any level this switch doesn't otherwise name
		return "medium"
	}
}

// codexReasoningSummary pairs model_reasoning_summary with the effort above:
// without it, a nonzero effort can still surface no reasoning summary in the
// stream. "auto" is codex's own documented value (let the model decide the
// summary's presence/verbosity) and is ctxloom's own choice, not a value this
// task verified from the binary — retunable later with no wire-schema break.
// off carries no summary override at all (ok=false): there is nothing to
// summarize when the caller asked for codex's lowest effort tier.
func codexReasoningSummary(level agent.ThinkingLevel) (value string, ok bool) {
	if level == agent.ThinkingOff {
		return "", false
	}
	return "auto", true
}
