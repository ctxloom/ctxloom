package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
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
// launch core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; ClaudeCode adds only the Claude-specific Configure/Execute.
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
	// claude's DECLARED minimal launch posture. Registering it here is what
	// makes it a declaration: Setup resolves it for a LaunchFormMinimal run and
	// buildArgs emits what Setup resolved, so no argv site reads a request flag
	// to decide whether this run is a headless one.
	b.SetMinimalLaunch(agent.MinimalArgsFunc(minimalModeArgs))
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
// runner's projection when the request carries one, else the plugin arm's,
// projected from the request — the harp the run env carries, the label's binary
// and args, the model, the mode and posture, the delivered MCP server names
// (the plan grant), the prompt, the working directory Setup recorded and
// the relocated home the run env names.
func (b *ClaudeCode) session(req *agent.ExecuteRequest) engine.Session {
	if req.Session != nil {
		return *req.Session
	}
	s := engine.Session{
		Identity:   sessions.Identity{Harp: req.Env[sessionHarpEnv]},
		Label:      engine.LabelConfig{Label: EngineName, Model: req.Model, Binary: b.BinaryPath, Args: b.Args},
		Mode:       req.Mode,
		Permission: req.Permissions,
		MCPServers: mcpServerNames(b.Resolved()),
		Prompt:     agent.GetPromptContent(req.Prompt),
		WorkDir:    b.WorkDir(),
	}
	if home := req.Env[ConfigDirEnv]; home != "" {
		s.Home = []engine.HomeBinding{{Var: ConfigDirEnv, Path: home}}
	}
	return s
}

// presented is what Setup delivered, as the presentations Exec reads: the
// out-of-cwd flag each delivered surface announced with the path it wrote
// (context, MCP, settings, in the resolved selection's order — argv order
// is observable: a VARIADIC claude flag landing last before a positional
// swallows it), then the minimal posture as an argv-only presentation when
// Setup resolved LaunchFormMinimal. A surface whose Path() is "" wrote
// nothing and presents nothing, so claude is never handed a flag naming a
// file that was not written.
func (b *ClaudeCode) presented() []present.Presentation {
	var out []present.Presentation
	if resolved := b.Resolved(); resolved != nil {
		noRoots := present.New(present.OnHost(present.Paths{}))
		for _, ra := range resolved.Approaches() {
			p, ok := ra.Approach.(pathed)
			if !ok || p.Path() == "" {
				continue
			}
			announced := ra.Approach.Present(noRoots).Args
			if len(announced) == 0 {
				continue
			}
			out = append(out, present.Presentation{HostPath: p.Path(), EnginePath: p.Path(), Args: []string{announced[0], p.Path()}})
		}
	}
	if minimal := b.MinimalArgs(); len(minimal) > 0 {
		out = append(out, present.Presentation{Args: minimal})
	}
	return out
}

// exec is the request's Exec: the instance bound to the request's session,
// over what the runner delivered (req.Presented) or, on the plugin arm,
// what Setup delivered.
func (b *ClaudeCode) exec(req *agent.ExecuteRequest) (engine.Exec, error) {
	inst, err := b.kind.Instance(b.session(req))
	if err != nil {
		return engine.Exec{}, err
	}
	presented := req.Presented
	if presented == nil {
		presented = b.presented()
	}
	return inst.Exec(presented)
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

	// Minimal oneshot runs with --output-format json: buffer the envelope,
	// emit the assistant text, and record the model the CLI actually used.
	// The branch bypasses the shared tail's routing, so it assembles its own
	// trace/env from the same helpers; dry-run still short-circuits first
	// (inside ExecuteCLI for the common path, here for this one).
	//
	// The test is on the argv THIS RUN ACTUALLY BUILT, not on a request flag
	// that separately implies it. Decoding a JSON envelope is only correct when
	// --output-format json was emitted, so asking the argv is asking the one
	// thing that can answer; a flag read here would be a second decision site
	// for a fact buildArgs already settled, free to disagree with it.
	ex, err := b.exec(req)
	if err != nil {
		return nil, err
	}
	b.SetExecuteEnv(func(*agent.ExecuteRequest) map[string]string { return ex.Env })
	args := ex.Args
	if !req.DryRun && req.Mode == agent.ModeOneshot && wantsJSONEnvelope(args) {
		b.TraceArgs(req.Verbosity, args, stderr)
		env := b.ExecuteEnv(req)
		var raw bytes.Buffer
		exitCode, err := b.RunNonInteractive(ctx, args, env, promptStdin(req), &raw, stderr)
		text, model, perr := parseClaudeJSONResult(raw.Bytes())
		if perr != nil {
			// Fault tolerant about the ENVELOPE: hand back whatever the CLI
			// emitted and keep the best-effort model rather than dropping the
			// result. Not tolerant about there being no result at all — see
			// below.
			payload := raw.Bytes()
			if werr := writeAll(stdout, payload); werr != nil && err == nil {
				err = werr
			}
			if oerr := requireOneshotOutput(payload, exitCode, err); oerr != nil {
				return &agent.ExecuteResult{ExitCode: oneshotFailureCode(exitCode), ModelInfo: modelInfo}, oerr
			}
			return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
		}
		if werr := writeAll(stdout, []byte(text)); werr != nil && err == nil {
			err = werr
		}
		if oerr := requireOneshotOutput([]byte(text), exitCode, err); oerr != nil {
			return &agent.ExecuteResult{ExitCode: oneshotFailureCode(exitCode), ModelInfo: modelInfo}, oerr
		}
		if model != "" {
			modelInfo.ModelName = model
		}
		return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
	}

	// Oneshot pipes the task on stdin (buildArgs left it off the argv); interactive
	// carries the prompt in argv and its stdin is the frontend's (req.Stdin).
	var oneshotStdin io.Reader
	if req.Mode == agent.ModeOneshot {
		oneshotStdin = promptStdin(req)
	}
	return b.ExecuteCLI(ctx, req, args, oneshotStdin, modelInfo, stdout, stderr)
}

// claudeJSONResult is the subset of the `claude --output-format json` envelope
// we consume: the assistant text and per-model token usage.
type claudeJSONResult struct {
	Result     string                      `json:"result"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
}

// claudeModelUsage is the per-model usage block. OutputTokens identifies the
// model that generated the result: the CLI may route a large read through an
// ancillary fast model (high input, tiny output) while the requested model does
// the actual generation, so output — not input — marks the working model.
// inputTokens is in the envelope too but this package never reads it;
// json.Unmarshal ignores it automatically, so it isn't modeled.
type claudeModelUsage struct {
	OutputTokens int `json:"outputTokens"`
}

// parseClaudeJSONResult extracts the result text and the resolved model id from
// a Claude CLI JSON envelope. The model is the modelUsage key with the most
// output tokens — the one that produced the result — so provenance records the
// generating model rather than a helper the CLI routed a read through. Ties
// break on sorted id for determinism.
// requireOneshotOutput turns "the minimal oneshot produced nothing" into a
// real failure.
//
// This branch is the distill/compaction path, and it could return ExitCode 0
// with a nil error having written ZERO bytes two ways: the CLI exits 0
// emitting nothing (the envelope then fails to parse, the empty buffer is
// copied through, and the nil error is returned), or the envelope parses fine
// with an empty "result". Both are indistinguishable from a working run to
// every exit-code gate above — which is exactly how a distillation that
// produced nothing gets written back over content that was fine.
//
// An error the run ALREADY reported is left alone: it is the more specific
// cause, and replacing it would hide why the run failed.
func requireOneshotOutput(payload []byte, exitCode int32, runErr error) error {
	if runErr != nil || len(bytes.TrimSpace(payload)) > 0 {
		return nil
	}
	return fmt.Errorf("claude produced no output (exit %d)", exitCode)
}

// oneshotFailureCode keeps the CLI's own non-zero exit code when it had one —
// that is the more specific signal — and otherwise synthesizes a failure,
// since exit 0 is precisely the lie requireOneshotOutput exists to stop.
func oneshotFailureCode(exitCode int32) int32 {
	if exitCode != 0 {
		return exitCode
	}
	return 1
}

// writeAll reports a short or failed write instead of discarding it. A
// partially delivered oneshot result is a truncated result, and the caller
// treats what it receives as complete.
func writeAll(w io.Writer, payload []byte) error {
	n, err := w.Write(payload)
	if err != nil {
		return fmt.Errorf("writing claude output: %w", err)
	}
	if n < len(payload) {
		return fmt.Errorf("writing claude output: wrote %d of %d bytes", n, len(payload))
	}
	return nil
}

func parseClaudeJSONResult(data []byte) (text, model string, err error) {
	var env claudeJSONResult
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", err
	}
	model = maxOutputModel(env.ModelUsage)
	return env.Result, model, nil
}

// maxOutputModel returns the model id with the largest output-token count,
// breaking ties on sorted id for determinism. Used to attribute a result to
// the GENERATING model among the CLI's per-model usage entries.
func maxOutputModel(m map[string]claudeModelUsage) string {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var best string
	bestTokens := -1
	for _, id := range ids {
		if n := m[id].OutputTokens; n > bestTokens {
			best, bestTokens = id, n
		}
	}
	return best
}

// sessionHarpEnv is the env var carrying ctxloom's per-session harp name (e.g.
// "fair-pushy-cable"). The host sets it on the run env; the backend reads it to
// name the launched claude session. Aliases the shared const in the agent
// substrate (which Setup also reads to place delivery scratch) so the two can't
// drift.
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

// wantsJSONEnvelope reports whether argv asks the CLI for the JSON result
// envelope, which is the only condition under which Execute may decode one. It
// reads the argv THIS RUN BUILT rather than any request flag that implies it,
// so the decode cannot get out of step with the emission: the mock engine
// discriminates the same way, off the same token, at the other end of the
// process boundary (the mock runtime's oneshotWantsJSON).
func wantsJSONEnvelope(args []string) bool {
	return argPair(args, flagOutputFormat, "json")
}

// minimalModeArgs is the distill/compaction posture: skip every unnecessary
// startup path while keeping the requested model in force.
func minimalModeArgs(model string) []string {
	return []string{
		// JSON envelope carries the resolved model id (modelUsage), letting
		// Execute record the real model instead of guessing. The result is
		// machine-consumed here, so we lose nothing by buffering it.
		flagOutputFormat, "json",
		flagTools, "", // Disable all tools
		flagNoSlashCommands,  // No slash commands
		flagNoSessionPersist, // Don't save session
		flagStrictMCPConfig,  // ignore .mcp.json / external MCP servers
		flagSystemPrompt, "", // drop CLAUDE.md/memory/identity so they don't pollute the result
		// Isolate via in-line overrides rather than `--setting-sources ""`:
		// an empty source list also drops the model config, so the CLI routes
		// generation to its built-in fast model regardless of --model. These
		// overrides disable hooks/MCP/attribution while leaving the requested
		// model in force.
		flagSettings, minimalSettings(model),
	}
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

// minimalSettings builds the JSON passed to `claude --settings` for headless
// distill/compaction. It overrides the loaded settings to an isolated baseline —
// no hooks, no project MCP, no attribution/cleanup, permissions bypassed — while
// keeping the requested model in force (an empty `--setting-sources` would drop
// the model config and route generation to the CLI's fast model). model is
// omitted when empty so the CLI default applies.
func minimalSettings(model string) string {
	s := map[string]any{
		"hooks":                      map[string]any{},
		"enableAllProjectMcpServers": false,
		"enabledMcpjsonServers":      []string{},
		"includeCoAuthoredBy":        false,
		"cleanupPeriodDays":          0,
		"permissions":                map[string]any{"defaultMode": "bypassPermissions"},
	}
	if model != "" {
		s["model"] = model
	}
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}
