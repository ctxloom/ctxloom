package claude

import (
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
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
}

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	kind, err := Build()
	if err != nil {
		panic(err) // the Definition literal is constant; conformance holds it valid
	}
	return newClaudeCode(kind.(Claude))
}

// newClaudeCode is the backend over one kind value (Claude.Backend).
func newClaudeCode(kind Claude) *ClaudeCode {
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
		nil, // SessionHistory: the legacy scraper was deleted; canonical capture is the only transcript source
		kind.Declaration(),
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
		Permission: defaultPolicy(),
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

// permissionArgs is a HEADLESS launch's posture on claude's argv: only what
// never changes between turns. bypass is the blanket skip; plain plan (no
// after_plan) keeps its read-only belt and MCP grant (below); every other
// mode rides each turn's --settings (turnSettings), so there is one
// mechanism for it and a later turn can change it. Nobody sits at a
// headless engine, so --permission-prompts none denies what the posture and
// rules leave open — whoever the approver is, until ctxloom serves the
// permission host that reaches them.
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
	return append(postureArgs(p, mcpServers), flagPermissionPrompts, "none")
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
// bypass, which stays on the argv), the declared rules, the grants ctxloom
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
