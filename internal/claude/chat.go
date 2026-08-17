package claude

import (
	"context"
	"maps"
	"strings"

	"github.com/ctxloom/ctxloom/internal/acp"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ClaudeACPAdapter is the ACP adapter binary that wraps claude for structured
// chat (Zed's adapter; claude-code has no native ACP mode). Located on PATH;
// ctxloom never installs binaries — an absent adapter yields the install hint
// Chat() reports below.
const ClaudeACPAdapter = "claude-code-acp"

// ClaudeACPTransport is this engine's ONE ACP-transport declaration. It lives
// in this package (not internal/lm/backends) so NewClaudeCode can set it on
// every instance it constructs — including direct construction outside the
// registry (delegation, tests) — via SetACPTransport, closing the zero-value
// footgun (an un-injected backend would default to ACPNative and skip the
// adapter). Every consumer reads the SAME value: this package's Chat() gate
// off the injected instance, and the registry's agentDescriptor (which reads
// claude.ClaudeACPTransport) for `ctxloom doctor`'s DOCTOR-CHECK-ACPADAPTER-m3
// and init PRIME's mirror. There is no import cycle: this package owns the
// value, and backends imports claude (never the reverse).
var ClaudeACPTransport = agent.ACPTransport{
	Kind:       agent.ACPAdapter,
	Binary:     ClaudeACPAdapter,
	InstallCmd: "npm install -g @zed-industries/claude-code-acp",
	Publisher:  "Zed Industries",
	SourceRepo: "https://github.com/zed-industries/claude-code-acp",
}

// Compile-time assertion that ClaudeCode offers the optional StructuredChat capability.
var _ agent.StructuredChat = (*ClaudeCode)(nil)

// Chat implements structured chat by delegating to the generic ACP driver over
// the claude-code-acp ADAPTER subprocess. This REPLACED the bespoke stream-json
// driver (chat_run.go/chat_stream.go, deleted once this path was verified live):
// one protocol mapper in internal/acp now serves every structured backend, and
// ACP's agent_thought_chunk CAN surface summarized thinking that stream-json
// stripped — but only when the adapter is actually asked to think (see
// withThinkingEnv below; this is the fix for the bug where ctxloom asked for
// nothing and thinking was never generated at all). Materialization stays with
// this backend (Setup wrote .claude/ + .mcp.json + the framed context file; the
// adapter runs claude in the same cwd, which reads them natively); the driver
// only speaks the protocol.
//
// The interactive terminal path (a real `claude` TUI over the pty launcher) is
// untouched — ACP replaces only the programmatic conversation surface.
func (b *ClaudeCode) Chat(ctx context.Context, req agent.ChatRequest, in <-chan agent.ChatMessage, out chan<- agent.ChatEvent) error {
	// The host-PATH adapter check only applies to a HOST-runtime chat: a
	// runtime:container agent runs claude-code-acp INSIDE the agent image
	// instead (ISO1's container transport, internal/acp), where the image
	// carries the adapter — checking THIS process's PATH would wrongly
	// refuse a session whose adapter the host never needs.
	//
	// transport is this backend's ONE ACP-transport declaration (agent.
	// ACPTransport), injected at construction by internal/lm/backends'
	// registry (SetACPTransport) — the SAME value ACPTransportFor("claude-code")
	// reports to callers with no backend instance (doctor_cmd.go). This
	// package cannot import backends itself (backends already imports claude
	// to register it — that would cycle), so the value arrives by injection
	// instead of a cross-package accessor call. RequireOnHost is the shared
	// gate every ACPAdapter engine runs (agent.ACPTransport's doc).
	transport := b.ACPTransport()
	if err := transport.RequireOnHost(req.Runtime, "claude"); err != nil {
		close(out) // honor the StructuredChat contract: producer closes out exactly once
		return err
	}
	// req.Env carries this call's caller-supplied env straight through to the
	// spawned adapter (internal/acp/session.go's spawnEnv method merges it
	// with ModelEnvVar); withThinkingEnv rides the SAME proven delivery path
	// rather than ACPConfig.Env, which — unlike ModelEnvVar/ModelConfigKey —
	// has no live per-chat application in the ACP driver for a
	// backend-resolved static value. Never mutates the caller's map.
	req.Env = withThinkingEnv(req.Env, b.thinking)
	// D-CO-QUIRK (CO1): quarantine the live model-selection bug into this one
	// named, version-scoped field rather than trusting ModelEnvVar alone (see
	// claudeModelSelectionQuirk's doc comment for the full defect and its
	// removal condition). Every OTHER backend's ChatRequest never sets this.
	req.ModelQuirk = claudeModelSelectionQuirk
	drv := acp.NewChatDriver(chatACPConfig(b.Env, transport.Binary))
	return drv.Chat(ctx, req, in, out)
}

// claudeThinkingEnvVar is the claude SDK's own extended-thinking budget
// selector. VERIFIED live (controlled A/B on one reasoning-forcing prompt):
// absent → 0 agent_thought_chunk frames; MAX_THINKING_TOKENS=10000 → 29. The
// Claude Agent SDK reads it — the bug this whole feature exists to fix is
// that ctxloom never set it, so structured chat NEVER generated thinking
// regardless of the model's own reasoning capability.
const claudeThinkingEnvVar = "MAX_THINKING_TOKENS"

// claudeThinkingTokens translates the normalized level into the SDK's env
// var value. These NUMBERS are ctxloom's own tuning, not Anthropic's: the raw
// Messages API REMOVED the token-count budget_tokens parameter for ctxloom's
// current default models (claude-opus-4-8, claude-sonnet-5 — sending it
// returns 400) in favor of a qualitative effort enum, which means the SDK is
// translating this env var internally rather than treating it as a literal
// token count — a future SDK release could silently re-scale it. 10000 is
// the verified-working value (the "think hard" tier); low/high are
// ctxloom's own proportional guesses, retunable later with no config-schema
// break since the wire vocabulary (off|low|medium|high) never changes.
// off returns ok=false so the caller sets no env var at all — the verified
// zero-thinking baseline — rather than a value claude might interpret as a
// non-zero budget.
func claudeThinkingTokens(level agent.ThinkingLevel) (value string, ok bool) {
	switch level {
	case agent.ThinkingOff:
		return "", false
	case agent.ThinkingLow:
		return "4000", true
	case agent.ThinkingHigh:
		return "24000", true
	default: // ThinkingMedium, and any level this switch doesn't otherwise name
		return "10000", true
	}
}

// withThinkingEnv returns env with claudeThinkingEnvVar added for a non-off
// level, added last: a caller-supplied override in env (unlikely, but always
// more specific than the resolved default) is NOT overwritten. env is never
// mutated — the caller's map (often req.Env, shared with other readers)
// stays untouched, matching internal/acp/session.go's own spawnEnv copy
// discipline for the analogous ModelEnvVar delivery.
func withThinkingEnv(env map[string]string, level agent.ThinkingLevel) map[string]string {
	value, ok := claudeThinkingTokens(level)
	if !ok {
		return env
	}
	if _, already := env[claudeThinkingEnvVar]; already {
		return env
	}
	out := make(map[string]string, len(env)+1)
	maps.Copy(out, env)
	out[claudeThinkingEnvVar] = value
	return out
}

// claudeModelSelectionQuirk is CO1's D-CO-QUIRK escape hatch: the ONE
// sanctioned fix for the live bug where every claude ACP session silently ran
// claude-opus-4-6 regardless of the requested model. Verified live (controlled
// experiment): claude-code-acp 0.16.2's SettingsManager reads only
// ~/.claude/settings.json (never ANTHROPIC_MODEL — the env var
// chatACPConfig's ModelEnvVar sets, and which this quirk does NOT replace,
// only supplements, since some future adapter release might start honoring
// it); its `--model` argv is silently ignored (zero `argv` hits in its
// dist/*.js); it does not implement session/set_config_option AT ALL (zero
// hits for that method name); and every session/new unconditionally calls
// query.setModel(...) from the adapter's OWN model list, falling back to
// models[0] — a CURRENT valid id fails identically to a stale one, so this is
// not a config problem this side can work around by supplying a "better"
// model string through the channels above.
//
// The ONE thing that works: claude-code-acp implements its JS-JSON-RPC-SDK's
// UNSTABLE `session/set_model` method — {sessionId, modelId} in,
// {} out — whose HANDLER is named unstable_setSessionModel(params) in
// dist/acp-agent.js:481-487 (that JS identifier is NOT the wire method name;
// the wire name comes from @agentclientprotocol/sdk's schema/index.js:
// `session_set_model: "session/set_model"` — confirmed live against the
// installed adapter, distinct from ANY method in the vendored spec schema
// this hub otherwise validates against, and distinct from the coder/acp-go-sdk
// fork ctxloom pins, which has no session/set_model constant at all — hence
// this quirk hand-rolls the request rather than using a fork-provided type).
// Live-testing confirmed it actually switches the model (requested "haiku"
// correctly ran claude-haiku-4-5-20251001). internal/acp/session.go's
// applyModelQuirk calls it immediately after session/new or session/load,
// before the first prompt — generically, with no claude-specific code of its
// own — because this quirk's AgentName/AdapterVersions match the connected
// agent's self-reported initialize identity exactly.
//
// REMOVAL CONDITION: delete this quirk (and applyModelQuirk's call sites in
// internal/acp/session.go) once a claude-code-acp release speaks
// session/set_config_option for model selection — the ecosystem is already
// moving (coder's acp-go-sdk v0.13.5 is titled "Session config options take
// over model selection"), so this SHOULD be short-lived. Before bumping the
// pinned adapter version, grep its dist/*.js for "set_config_option"; if it's
// there, drop the old version from AdapterVersions (or delete the whole
// quirk if nothing else needs it) instead of silently widening the list.
var claudeModelSelectionQuirk = &agent.ModelDeliveryQuirk{
	Method:          "session/set_model",
	AgentName:       "@zed-industries/claude-code-acp",
	AdapterVersions: []string{"0.16.2"},
}

// chatACPConfig is the adapter config for one claude structured-chat spawn.
// binary is the adapter command to spawn — Chat() passes its own
// b.ACPTransport().Binary rather than this function reading ClaudeACPAdapter
// directly, so every caller (including tests) says explicitly which
// transport declaration it's exercising.
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
func chatACPConfig(env map[string]string, binary string) acp.ACPConfig {
	return acp.ACPConfig{
		Command:  binary,
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
