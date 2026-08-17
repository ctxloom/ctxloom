package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
	// Thinking is the normalized reasoning/thinking-budget level
	// (off|low|medium|high — agent.ThinkingLevel). Empty or unrecognized
	// defaults to "medium". See chat.go's translation to claude's
	// MAX_THINKING_TOKENS env var — the load-bearing fix for the bug where
	// ctxloom never set that variable at all, so claude-code-acp never
	// generated any thinking (0 thought chunks, verified live).
	Thinking string `mapstructure:"thinking"`
}

// BackendType identifies the backend this config drives.
func (ClaudeConfig) BackendType() string { return "claude-code" }

// GetEnv returns the labeled entry's env map. Lets shared code (see
// operations.LLMEnvFor) reach a decoded config's Env through an interface
// assertion instead of a concrete-type switch — internal/operations may not
// import engine plugin packages directly (ADR-0026).
func (c ClaudeConfig) GetEnv() map[string]string { return c.Env }

// ClaudeCode implements the Backend interface for Claude Code CLI. The shared
// launch core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; ClaudeCode adds only the Claude-specific Configure/Execute.
type ClaudeCode struct {
	agent.LaunchBackend
	// surfaces is the SurfaceSet Setup built for the current run, stashed by the
	// Build closure so buildArgs can read each out-of-cwd file's Path() (the
	// --append-system-prompt-file / --mcp-config / --settings scratch a SharedCell
	// delivered) after Setup ran. Zero value (nil surface fields) before Setup.
	surfaces Surfaces
	// thinking is the resolved normalized reasoning level (Configure defaults
	// it to agent.ThinkingMedium — the Go zero value happens to be
	// ThinkingOff, so an unconfigured backend must NOT rely on the zero
	// value; NewClaudeCode sets it explicitly). Chat translates it into
	// claude's MAX_THINKING_TOKENS env var (chat.go).
	thinking agent.ThinkingLevel
}

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	b := &ClaudeCode{}
	b.BaseBackend = agent.NewBaseBackend("claude-code", "1.0.0")
	b.BinaryPath = "claude"
	// claude routes launch-time surface delivery through the surfaces × cells
	// seam. In a SharedCell context/MCP/settings ride out-of-cwd launch flags (the
	// SessionStart context-injection hook stays suppressed); in an isolated cell
	// they land as well-known files in the private working dir. The Build closure
	// stashes the concrete Surfaces so buildArgs can read the flag files' paths.
	b.InitLaunch(
		agent.NewBaseLifecycle("claude-code"),
		agent.NewBaseContextProvider(),
		nil, // SessionHistory: claude's ~/.claude/projects/*.jsonl scraper deleted — canonical capture is the only transcript source now
		&agent.CellDelivery{Build: b.buildSurfaces},
	)
	b.SetACPTransport(ClaudeACPTransport) // intrinsic: every construction path (incl. direct) gets it
	return b
}

// buildSurfaces is claude's CellDelivery.Build closure: it maps the shared
// per-run inputs to claude's SurfaceSet, targeting isolatedDir for the out-of-cwd
// race-safe files, and stashes the concrete Surfaces on the backend so buildArgs
// can read Context/MCP/Settings.Path() after a SharedCell delivery.
func (b *ClaudeCode) buildSurfaces(in agent.SurfaceInputs, isolatedDir string) agent.SurfaceSet {
	b.surfaces = NewSurfaces(in, dirPlacement{dir: isolatedDir}, nil)
	return b.surfaces
}

// Configure applies a decoded claude-code config to this backend.
func (b *ClaudeCode) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*ClaudeConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
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
	if !req.DryRun && req.Mode == agent.ModeOneshot && req.SkipSetup {
		args := b.buildArgs(req)
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
	return b.ExecuteCLI(ctx, req, b.buildArgs(req), oneshotStdin, modelInfo, stdout, stderr)
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

// sessionNameArgs returns the `--name <harp>` flag pair that labels the launched
// claude session with ctxloom's harp name, or nil when no harp is set. claude's
// /rename slash command is interactive-only and cannot be injected
// programmatically or as an initial prompt, so --name is the only launch-time way to
// set the session's display name (prompt box, /resume picker, terminal title).
func sessionNameArgs(env map[string]string) []string {
	if harp := env[sessionHarpEnv]; harp != "" {
		return []string{flagName, harp}
	}
	return nil
}

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
// list still holds. The generalized permission posture has no per-tool
// policy input yet, so the set is a fixed, conservative write/exec list —
// Bash (arbitrary exec, including file writes via shell), Edit, Write,
// NotebookEdit (every built-in mutating tool this codebase's own tool
// vocabulary names).
func permissionArgs(mode agent.PermissionMode) []string {
	switch mode {
	case agent.PermissionBypass:
		return []string{flagSkipPermissions}
	case agent.PermissionAcceptEdits:
		return []string{flagPermissionMode, "acceptEdits"}
	case agent.PermissionPlan:
		return []string{flagPermissionMode, "plan", flagDisallowedTools, "Bash,Edit,Write,NotebookEdit"}
	}
	return nil
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

// buildArgs constructs the command-line arguments.
func (b *ClaudeCode) buildArgs(req *agent.ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	args = append(args, permissionArgs(req.Permissions)...)

	// The model is resolved by the caller (the fast role's labeled config for
	// compression, the primary role's for coding); the backend no longer
	// substitutes a fast-model default. An empty model lets the CLI pick.
	if req.Model != "" {
		args = append(args, flagModel, req.Model)
	}

	// Name the interactive session after ctxloom's harp so claude's prompt box,
	// /resume picker, and terminal title match the session identity. Oneshot and
	// minimal runs are throwaway (often --no-session-persistence), so they stay
	// unnamed.
	if req.Mode == agent.ModeInteractive {
		args = append(args, sessionNameArgs(req.Env)...)
	}

	if req.Mode == agent.ModeOneshot {
		args = append(args, flagPrint)
	}

	// Cell-aware native delivery. In a SharedCell (the user's live cwd) Setup
	// delivered context/MCP/settings as out-of-cwd scratch files, so point claude's
	// own launch flags at them — --append-system-prompt-file for the framed context
	// (in place of a SessionStart injection hook), --mcp-config for the managed MCP
	// set, and --settings for the managed hooks/statusline. --mcp-config is used
	// WITHOUT --strict-mcp-config so claude LAYERS ctxloom's out-of-cwd servers on
	// top of (i.e. merges them with) the user's project .mcp.json rather than
	// replacing it — ctxloom stays out of the cwd while the user's own servers still
	// load. (--settings likewise layers over the user's .claude/settings.json.) Each
	// Path() is "" when that surface delivered nothing (empty context/MCP/hooks) or
	// when context fell back to the injection hook, so buildArgs then adds no flag.
	// In an isolated cell the surfaces are the engine's well-known files in cwd, so
	// no flags are needed. Skipped in minimal/distill mode (SkipSetup), which drops
	// context and supplies its own --settings/--strict-mcp-config below.
	if !req.SkipSetup && req.CellKind == agent.CellKindShared {
		args = append(args, b.surfaces.flagArgs()...)
	}

	// Minimal mode for distillation/compaction - skip all unnecessary startup.
	if req.SkipSetup {
		args = append(args, minimalModeArgs(req.Model)...)
	}

	// Interactive delivers the initial prompt as an argv positional (it's short —
	// a human typed it). Oneshot pipes the task on stdin instead (see promptStdin /
	// Execute), so a large task (a diff to review, a session to compact) can't
	// exceed the OS argv length limit — the E2BIG that broke `ctxloom weave`
	// synthesis. buildArgs omits it here for oneshot; claude -p reads it from stdin.
	//
	// The prompt is preceded by an explicit "--" terminator. Several of the
	// flags above are VARIADIC in claude's own parser (`claude --help` declares
	// --disallowedTools <tools...>, --tools <tools...>, --mcp-config
	// <configs...>), so whichever one lands last before the positional
	// SWALLOWS the prompt as flag values. Live repro, claude 2.1.220:
	//
	//	claude -p --tools "" --disallowedTools "Bash,Edit" "Reply with exactly: PROMPTOK"
	//	  -> Permission deny rule "Reply" matches no known tool  (x4)
	//	     Error: Input must be provided...
	//
	// and the same prompt turned into a path with --mcp-config. The reachable
	// argv is ordinary: PermissionPlan + interactive + no --model + no harp +
	// an isolated cell (which skips the surface flags above) emits
	// `--permission-mode plan --disallowedTools Bash,Edit,Write,NotebookEdit
	// <prompt>`. Before the terminator, safety here was incidental — it held
	// only while --model or --name happened to follow. "--" is emitted only
	// when there IS a positional; a trailing bare "--" would be
	// noise, and claude has no other positional to protect.
	if req.Mode == agent.ModeInteractive {
		if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
			args = append(args, "--", prompt)
		}
	}

	return args
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
