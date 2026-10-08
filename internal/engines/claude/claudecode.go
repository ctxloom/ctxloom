package claude

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/kit"
)

// ClaudeConfig is claude-code's typed LLM config: the fields a claude-code
// labeled entry may carry. The backend owns this struct; the config package
// only carries the raw body that decodes into it.
type ClaudeConfig struct {
	// Model is intentionally NOT a field here: the effective model
	// is read untyped from the same YAML body via config.ResolveLLM
	// (entry.Body["model"].(string)) — mapstructure would silently accept an
	// unknown "model" key even without a matching field, so a typed field
	// here would just be a second, dead reader of the same key.
	BinaryPath string   `mapstructure:"binary_path"`
	Args       []string `mapstructure:"args"`
}

// BackendType identifies the backend this config drives.
func (ClaudeConfig) BackendType() string { return EngineName }

// ClaudeCode is claude's agent.Backend: the shared projection onto the kind
// (kit.ProjectedBackend) with claude's defaults (claudeBackend).
type ClaudeCode = kit.ProjectedBackend

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	kind, err := Build()
	if err != nil {
		panic(err) // the Definition literal is constant; conformance holds it valid
	}
	return kit.NewProjectedBackend(kind, nil, claudeBackend)
}

// claudeBackend is what claude's projected backend needs beyond the kind.
// The run's CLAUDE_CONFIG_DIR is the engine home a request names.
var claudeBackend = kit.BackendDefaults{
	Engine:   EngineName,
	Provider: "anthropic",
	HomeVar:  ConfigDirEnv,
	Policy:   defaultPolicy,
	Local: func(cfg agent.BackendConfig) ([]string, error) {
		c, ok := cfg.(*ClaudeConfig)
		if !ok {
			return nil, kit.ErrConfigType(EngineName, cfg)
		}
		return c.Args, nil
	},
}

// permissionArgs is a HEADLESS launch's posture on claude's argv: only what
// never changes between turns. bypass is the blanket skip; plain plan (no
// after_plan) keeps its read-only belt and MCP grant (below); every other
// mode rides each turn's --settings (turnSettings), so there is one
// mechanism for it and a later turn can change it. Nobody sits at a
// headless engine, so what the posture and rules leave open goes to the
// human at the root through the PermissionRequest hook, or is denied
// (promptArgs).
//
// A plan-first posture (plan with after_plan) drops the belt and the argv
// grant: an approved plan must be able to execute (claude's plan mode is
// read-only on its own), and an argv grant would outlive the plan turn as
// blanket permission for the server — the MCP grant rides the plan turns'
// --settings instead.
//
// plan ALSO gets a conservative --disallowedTools belt-and-suspenders
// (LIVE VERIFIED against authenticated claude 2.1.210: `--permission-mode
// plan --disallowedTools "Bash,Edit,Write,NotebookEdit"` denied a
// sentinel-file overwrite — byte-unchanged, model explicitly cited BOTH
// plan mode and the missing write tools). --permission-mode plan is
// already a genuine read-only posture on its own, so this is
// defense-in-depth, not the fix itself: if a future
// claude release ever narrows plan's own semantics, the explicit deny
// list still holds. The DENY set stays fixed and conservative — Bash
// (arbitrary exec, including file writes via shell), Edit, Write,
// NotebookEdit (every built-in mutating tool this codebase's own tool
// vocabulary names) — because it names BUILT-IN tools, which do not vary
// by launch. The grant below does vary, which is why it takes an argument
// and this does not.
//
// plan ALSO gets an --allowedTools GRANT naming each attached MCP SERVER,
// because plan gates an MCP call TWICE and the two gates are independent.
// Measured against claude 2.1.251, one server, one run, varying one thing:
//
//	hint + grant    -> the call SUCCEEDS
//	hint, no grant  -> "requested permissions ... but you haven't granted it yet"
//	grant, no hint  -> "Cannot call <tool> while in plan mode"
//
// So the readOnlyHint stamped at registration is necessary but NOT sufficient.
// Without this grant a plan agent reaches none of its MCP tools — not even
// search_content — which is the state this replaced.
//
// The grant is deliberately SERVER-level (mcp__<server>, no tool suffix) even
// though it reads as coarser than naming tools. Two reasons, and the first is
// why it is not actually coarser:
//
//   - The GATES COMPOSE. Measured on one server with one server-level grant:
//     an annotated tool succeeded and an unannotated one still returned
//     "Cannot call ... while in plan mode". readOnlyHint stays the real
//     per-tool filter; the grant only says which servers are in play.
//   - It is the only form that can cover COMPANION servers. ctxloom does not
//     know taskloom's or a bundle-supplied server's tool inventory, so it
//     cannot enumerate their read-only tools — but it does know which servers
//     it attached. A companion that annotates honestly gets its read tools
//     through; one that annotates nothing gets nothing, which is the safe way
//     to be wrong.
//
// This is therefore plan-ONLY. Under a posture with no read-only tier the
// hint gate is absent, and a server-level grant WOULD be blanket permission
// for that server's mutating tools.
// The delivered bundle map (mcpServerNames, surfaces.go) is the honest
// answer to "which servers is this argv wiring up": it already carries
// ctxloom's own server alongside the config- and bundle-supplied ones, which
// is why it is read there rather than the ctxloom name being appended
// separately. A server attached but not named is a server a plan agent
// cannot reach.

func permissionArgs(p posture, mcpServers []string) []string {
	return append(postureArgs(p, mcpServers), promptArgs(p)...)
}

// promptArgs says who answers, in a run nobody sits at, what the posture and
// rules leave open. The human at the root: NO prompt flag, so claude awaits
// the PermissionRequest hook, honours its decision and denies what no hook
// decided — the argv the P12 rung's allow, deny and silent cells pin.
// Otherwise nobody, so it is denied: approver none or reviewer (claude's own
// dontAsk and auto decide), and bypass, which asks nobody anything; those
// runs carry no approval hook, and --permission-prompts none denies locally
// (P12's allow-prompts-none cell shows none still consults a hook first).
func promptArgs(p posture) []string {
	if p.approver == engine.ApproverHuman && p.mode != modeBypass {
		return nil
	}
	return []string{flagPermissionPrompts, "none"}
}

// postureArgs are the posture flags every launch carries, headless or not:
// bypass's blanket skip, and plain plan's read-only belt with its MCP grant.
func postureArgs(p posture, mcpServers []string) []string {
	switch {
	case p.mode == modeBypass:
		return []string{flagSkipPermissions}
	case p.mode == modePlan && !p.planFirst():
		args := []string{flagDisallowedTools, "Bash,Edit,Write,NotebookEdit"}
		// No attached servers means no grant to make. Emitting the flag with
		// an empty value would declare "grant nothing" to a VARIADIC parser
		// sitting next to a positional — the argv hazard buildArgs documents.
		if granted := agent.QualifyMCPServers(mcpServers); len(granted) > 0 {
			args = append(args, flagAllowedTools, strings.Join(granted, ","))
		}
		return args
	}
	return nil
}

// interactivePermissionArgs is the human's own session's posture: the mode
// on --permission-mode (default adds nothing), the posture flags, and the
// declared rules and sandbox on one inline --settings. Nobody is told to
// deny prompts: the human answers them, unless the approver says otherwise
// (claudeMode).
func interactivePermissionArgs(p posture, mcpServers []string) ([]string, error) {
	var args []string
	mode, err := p.claudeMode(true)
	if err != nil {
		return nil, err
	}
	if mode != modeDefault && mode != modeBypass {
		args = append(args, flagPermissionMode, mode)
	}
	args = append(args, postureArgs(p, mcpServers)...)
	doc, err := settingsDocument(permissionsDoc{Allow: p.allow, Deny: p.deny, Ask: p.ask}, p.sandboxDoc())
	if err != nil || doc == "" {
		return args, err
	}
	return append(args, flagSettings, doc), nil
}

// permissionsDoc is the permissions member of a claude settings document.
type permissionsDoc struct {
	DefaultMode string   `json:"defaultMode,omitempty"`
	Allow       []string `json:"allow,omitempty"`
	Deny        []string `json:"deny,omitempty"`
	Ask         []string `json:"ask,omitempty"`
}

// settingsDocument renders a settings document carrying only permissions
// and the sandbox, as the inline JSON --settings takes; "" when there is
// nothing to say.
func settingsDocument(p permissionsDoc, sandbox *sandboxDoc) (string, error) {
	out := map[string]any{}
	if p.DefaultMode != "" || len(p.Allow)+len(p.Deny)+len(p.Ask) > 0 {
		out["permissions"] = p
	}
	if sandbox != nil {
		out["sandbox"] = sandbox
	}
	if len(out) == 0 {
		return "", nil
	}
	b, err := json.Marshal(out)
	return string(b), err
}

// turnSettings is one headless turn's --settings: the turn's mode (never
// bypass, which stays on the argv; always named, so no other settings
// source's defaultMode applies), the declared rules, the grants ctxloom
// holds for the run and the sandbox — plus, on a plan-first session's plan
// turn, the MCP grant plan needs (see postureArgs). A declared deny or ask
// still wins over a grant: claude evaluates deny, then ask, then allow.
func turnSettings(p posture, mcpServers []string, t engine.TurnPosture) (string, error) {
	doc := permissionsDoc{Deny: p.deny, Ask: p.ask}
	mode := t.Mode
	if mode == "" {
		m, err := p.claudeMode(false)
		if err != nil {
			return "", err
		}
		mode = m
	}
	if mode != modeBypass {
		doc.DefaultMode = mode
	}
	doc.Allow = append(slices.Clone(p.allow), t.Grants...)
	if mode == modePlan && p.planFirst() {
		doc.Allow = append(doc.Allow, agent.QualifyMCPServers(mcpServers)...)
	}
	return settingsDocument(doc, p.sandboxDoc())
}
