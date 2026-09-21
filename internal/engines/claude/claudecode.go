package claude

import (
	"context"
	"io"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
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
	// Thinking is the normalized reasoning/thinking-budget level
	// (off|low|medium|high — agent.ThinkingLevel). Empty or unrecognized
	// defaults to "medium". Accepted and validated; no launch of this
	// package carries it (see ClaudeCode.thinking).
	Thinking string `mapstructure:"thinking"`
}

// BackendType identifies the backend this config drives.
func (ClaudeConfig) BackendType() string { return EngineName }

// ClaudeCode implements the Backend interface for Claude Code CLI. The shared
// launch core (capability wiring, accessors, the Execute tail) lives in the
// embedded agent.LaunchBackend; ClaudeCode adds only the Claude-specific
// Configure/Execute.
type ClaudeCode struct {
	agent.LaunchBackend
	// kind is the engine KIND this backend projects requests onto: every
	// argv this backend runs is kind.Instance(session).Exec(presented).
	kind engine.Engine
	// gate tracks whether claude is currently showing a modal, so a
	// coordinator wake is withheld rather than answering the prompt for the
	// human. It satisfies agent.InputGate; see inputgate.go for the
	// measurement it rests on. Kept by VALUE, so a ClaudeCode must not be
	// copied once in use — nothing copies one today (it is always *ClaudeCode).
	gate inputGate
	// thinking is the resolved normalized reasoning level from the label's
	// config (Configure defaults it to agent.ThinkingMedium). NOTHING READS
	// IT: no argv or env of this package carries it, so the knob is accepted
	// and validated but has no effect on a launch.
	thinking agent.ThinkingLevel
}

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	kind, err := Build()
	if err != nil {
		panic(err) // the Definition literal is constant; conformance holds it valid
	}
	b := &ClaudeCode{kind: kind}
	b.BaseBackend = agent.NewBaseBackend(EngineName, "1.0.0")
	b.BinaryPath = "claude"
	// claude routes launch-time surface delivery through the surfaces × cells
	// seam. In a SharedCell context/MCP/settings ride out-of-cwd launch flags (the
	// SessionStart context-injection hook stays suppressed); in an isolated cell
	// they land as well-known files in the private working dir. The Build closure
	// stashes the concrete Surfaces so buildArgs can read the flag files' paths.
	b.InitLaunch(
		agent.NewBaseLifecycle(EngineName),
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: retired; the hosting record's NoLegacyHistoryReason says why
		Declaration(),
	)
	// The run's CLAUDE_CONFIG_DIR is the engine home the record-backed
	// settings write (surfaces_hewrecord.go) lands beneath; a run without one
	// advises no engine home and that write refuses.
	b.SetEngineHomeVar(ConfigDirEnv)
	return b
}

// Configure applies a decoded claude-code config to this backend.
func (b *ClaudeCode) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*ClaudeConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args)
		// An unrecognized (but non-empty) value still resolves to the
		// documented medium default (ParseThinkingLevel's ok=false path) —
		// advisory validation, matching agents.SetAgentRequest's tolerance for
		// a typo'd permissions/runtime value, never a hard failure over a
		// cost-tuning knob.
		level, ok := agent.ParseThinkingLevel(c.Thinking)
		if c.Thinking != "" && !ok {
			clidiag.Warn("ctxloom", "claude-code config declares unknown thinking level %q (known: %s); using the default %q",
				c.Thinking, strings.Join(agent.ThinkingLevelNames(), "|"), agent.ThinkingMedium)
		}
		b.thinking = level
	}
}

// session is the engine-facing Session the instance is bound to: the
// runner's projection when the request carries one, else one projected from
// the request — the harp the run env carries, the label's binary and args,
// the model, the mode and posture, the prompt, the working directory and the
// relocated home the run env names.
func (b *ClaudeCode) session(req *agent.ExecuteRequest) engine.Session {
	if req.Session != nil {
		return *req.Session
	}
	s := engine.Session{
		Identity:   sessions.Identity{Harp: req.Env[sessionHarpEnv]},
		Label:      engine.LabelConfig{Label: EngineName, Model: req.Model, Binary: b.BinaryPath, Args: b.Args},
		Mode:       req.Mode,
		Permission: req.Permissions,
		Prompt:     agent.GetPromptContent(req.Prompt),
		WorkDir:    b.WorkDir(),
	}
	if home := req.Env[ConfigDirEnv]; home != "" {
		s.Home = []engine.HomeBinding{{Var: ConfigDirEnv, Path: home}}
	}
	return s
}

// exec is the request's Exec: the instance bound to the request's session,
// over what the runner delivered (req.Presented).
func (b *ClaudeCode) exec(req *agent.ExecuteRequest) (engine.Exec, error) {
	inst, err := b.kind.Instance(b.session(req))
	if err != nil {
		return engine.Exec{}, err
	}
	return inst.Exec(req.Presented)
}

// Execute runs the backend with the given request.
func (b *ClaudeCode) Execute(ctx context.Context, req *agent.ExecuteRequest, stdout, stderr io.Writer) (*agent.ExecuteResult, error) {
	// Best-effort model identity. In minimal mode this is overwritten below with
	// the real id the CLI reports; otherwise it is the requested model, falling
	// back to the backend name rather than a fabricated version.
	modelName := req.Model
	if modelName == "" {
		modelName = b.Name()
	}
	modelInfo := &agent.ModelInfo{
		ModelName: modelName,
		Provider:  "anthropic",
	}

	ex, err := b.exec(req)
	if err != nil {
		return nil, err
	}
	b.SetExecuteEnv(func(*agent.ExecuteRequest) map[string]string { return ex.Env })
	args := ex.Args

	// Oneshot pipes the task on stdin (buildArgs left it off the argv); interactive
	// carries the prompt in argv and its stdin is the frontend's (req.Stdin).
	var oneshotStdin io.Reader
	if req.Mode == agent.ModeOneshot {
		oneshotStdin = promptStdin(req)
	}
	return b.ExecuteCLI(ctx, req, args, oneshotStdin, modelInfo, stdout, stderr)
}

// sessionHarpEnv is the env var carrying ctxloom's per-session harp name (e.g.
// "fair-pushy-cable"). The host sets it on the run env; the backend reads it to
// name the launched claude session. Aliases the shared const in the agent
// substrate so the two can't drift.
const sessionHarpEnv = agent.SessionHarpEnv

// permissionArgs maps the generalized permission posture onto claude's flags.
// bypass is the blanket skip; acceptEdits/plan use --permission-mode; default
// leaves the engine's normal prompting and adds nothing.
//
// plan ALSO gets a conservative --disallowedTools belt-and-suspenders
// (LIVE VERIFIED against authenticated claude 2.1.210: `--permission-mode
// plan --disallowedTools "Bash,Edit,Write,NotebookEdit"` denied a
// sentinel-file overwrite — byte-unchanged, model explicitly cited BOTH
// plan mode and the missing write tools). --permission-mode plan is
// already a genuine read-only posture on its own (unlike kiro's
// collapsed read-only, this is the LESS broken of the under-mapped
// engines), so this is defense-in-depth, not the fix itself: if a future
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

func permissionArgs(mode agent.PermissionMode, mcpServers []string) []string {
	switch mode {
	case agent.PermissionBypass:
		return []string{flagSkipPermissions}
	case agent.PermissionAcceptEdits:
		return []string{flagPermissionMode, "acceptEdits"}
	case agent.PermissionPlan:
		args := []string{
			flagPermissionMode, "plan",
			flagDisallowedTools, "Bash,Edit,Write,NotebookEdit",
		}
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

// buildArgs is the request's argv: Instance.Exec's, and nothing composed
// here (TestBuildArgs_IsInstanceExec pins the delegation; the launch golden
// pins the bytes).
func (b *ClaudeCode) buildArgs(req *agent.ExecuteRequest) []string {
	ex, err := b.exec(req)
	if err != nil {
		panic(err) // the kind carries a context surface; Instance cannot refuse it
	}
	return ex.Args
}

// promptStdin returns the oneshot task as a stdin reader for claude -p, or nil
// when there is no prompt. Delivering the task on stdin instead of argv keeps a
// large prompt off the command line, which the OS length-limits (E2BIG).
func promptStdin(req *agent.ExecuteRequest) io.Reader {
	if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
		return strings.NewReader(prompt)
	}
	return nil
}
