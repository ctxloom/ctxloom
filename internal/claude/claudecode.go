package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// ClaudeConfig is claude-code's typed LLM config: the fields a claude-code
// labeled entry may carry. The backend owns this struct; the config package
// only carries the raw body that decodes into it.
type ClaudeConfig struct {
	Model      string            `mapstructure:"model"`
	BinaryPath string            `mapstructure:"binary_path"`
	Args       []string          `mapstructure:"args"`
	Env        map[string]string `mapstructure:"env"`
}

// BackendType identifies the backend this config drives.
func (ClaudeConfig) BackendType() string { return "claude-code" }

// ClaudeCode implements the Backend interface for Claude Code CLI. The shared
// launch core (capability wiring, accessors, Setup/Cleanup) lives in the embedded
// agent.LaunchBackend; ClaudeCode adds only the Claude-specific Configure/Execute.
type ClaudeCode struct {
	agent.LaunchBackend
	writeSettings agent.WriteSettingsFunc
	// factory is the delivery factory Setup drives to materialize claude's
	// surfaces through the runner-side seam. The SAME instance is injected into
	// the base (via InitLaunch) and held here so buildArgs can read the framed
	// context file's path (factory.ContextPath) after Setup ran.
	factory *deliveryFactory
}

// NewClaudeCode creates a new Claude Code backend with default settings. The
// writeSettings dispatch is injected (the registry supplies it) so the launch
// bases can write settings without importing the registry.
func NewClaudeCode(writeSettings agent.WriteSettingsFunc) *ClaudeCode {
	b := &ClaudeCode{writeSettings: writeSettings}
	b.BaseBackend = agent.NewBaseBackend("claude-code", "1.0.0")
	b.BinaryPath = "claude"
	// claude routes launch-time surface delivery through the seam: context lands
	// in the session's private ephemeral dir and rides --append-system-prompt-file
	// (buildArgs), so the SessionStart context-injection hook is suppressed.
	b.factory = newDeliveryFactory(nil)
	b.InitLaunch(
		agent.NewBaseLifecycle("claude-code", b.writeSettings),
		&ClaudeSkills{},
		agent.NewBaseContextProvider(),
		NewClaudeSessionHistory(b),
		b.factory,
	)
	return b
}

// Configure applies a decoded claude-code config to this backend.
func (b *ClaudeCode) Configure(cfg agent.BackendConfig) {
	if c, ok := cfg.(*ClaudeConfig); ok {
		agent.ApplyLocalCLIConfig(&b.BaseBackend, c.BinaryPath, c.Args, c.Env)
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
			// Fault tolerant: hand back whatever the CLI emitted and keep the
			// best-effort model rather than dropping the result.
			_, _ = stdout.Write(raw.Bytes())
			return &agent.ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
		}
		_, _ = io.WriteString(stdout, text)
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
type claudeModelUsage struct {
	InputTokens  int `json:"inputTokens"`
	OutputTokens int `json:"outputTokens"`
}

// parseClaudeJSONResult extracts the result text and the resolved model id from
// a Claude CLI JSON envelope. The model is the modelUsage key with the most
// output tokens — the one that produced the result — so provenance records the
// generating model rather than a helper the CLI routed a read through. Ties
// break on sorted id for determinism.
func parseClaudeJSONResult(data []byte) (text, model string, err error) {
	var env claudeJSONResult
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", err
	}
	model, _ = pickByMaxOutput(env.ModelUsage, func(u claudeModelUsage) int { return u.OutputTokens })
	return env.Result, model, nil
}

// pickByMaxOutput returns the map entry with the largest output measure,
// breaking ties on sorted key for determinism. Used to attribute a result to
// the GENERATING model among the CLI's per-model usage entries.
func pickByMaxOutput[T any](m map[string]T, out func(T) int) (string, T) {
	ids := make([]string, 0, len(m))
	for id := range m {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var best string
	var bestVal T
	bestTokens := -1
	for _, id := range ids {
		if n := out(m[id]); n > bestTokens {
			best, bestVal, bestTokens = id, m[id], n
		}
	}
	return best, bestVal
}

// sessionHarpEnv is the env var carrying ctxloom's per-session harp name (e.g.
// "fair-pushy-cable"). The host sets it on the run env; the backend reads it to
// name the launched claude session. Aliases the shared const in the agent
// substrate (which Setup also reads to place delivery scratch) so the two can't
// drift.
const sessionHarpEnv = agent.SessionHarpEnv

// sessionNameArgs returns the `--name <harp>` flag pair that labels the launched
// claude session with ctxloom's harp name, or nil when no harp is set. claude's
// /rename slash command is interactive-only and cannot be injected over
// stream-json or as an initial prompt, so --name is the only launch-time way to
// set the session's display name (prompt box, /resume picker, terminal title).
func sessionNameArgs(env map[string]string) []string {
	if harp := env[sessionHarpEnv]; harp != "" {
		return []string{"--name", harp}
	}
	return nil
}

// buildArgs constructs the command-line arguments.
func (b *ClaudeCode) buildArgs(req *agent.ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	// Map the generalized permission posture onto claude's flags. bypass is the
	// blanket skip; acceptEdits/plan use --permission-mode; default leaves the
	// engine's normal prompting.
	switch req.Permissions {
	case agent.PermissionBypass:
		args = append(args, "--dangerously-skip-permissions")
	case agent.PermissionAcceptEdits:
		args = append(args, "--permission-mode", "acceptEdits")
	case agent.PermissionPlan:
		args = append(args, "--permission-mode", "plan")
	}

	// The model is resolved by the caller (the fast role's labeled config for
	// compression, the primary role's for coding); the backend no longer
	// substitutes a fast-model default. An empty model lets the CLI pick.
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	// Name the interactive session after ctxloom's harp so claude's prompt box,
	// /resume picker, and terminal title match the session identity. Oneshot and
	// minimal runs are throwaway (often --no-session-persistence), so they stay
	// unnamed.
	if req.Mode == agent.ModeInteractive {
		args = append(args, sessionNameArgs(req.Env)...)
	}

	if req.Mode == agent.ModeOneshot {
		args = append(args, "--print")
	}

	// Native context delivery: load ctxloom's assembled context from the framed
	// file Setup delivered to the session's ephemeral dir, via claude's own
	// --append-system-prompt-file, in place of a SessionStart injection hook.
	// Skipped in minimal/distill mode (SkipSetup), which intentionally drops
	// context. Empty when there was no context, or when delivery fell back to the
	// injection hook — buildArgs then adds no flag.
	if !req.SkipSetup {
		if p := b.factory.ContextPath(); p != "" {
			args = append(args, "--append-system-prompt-file", p)
		}
	}

	// Minimal mode for distillation/compaction - skip all unnecessary startup.
	if req.SkipSetup {
		args = append(args,
			// JSON envelope carries the resolved model id (modelUsage), letting
			// Execute record the real model instead of guessing. The result is
			// machine-consumed here, so we lose nothing by buffering it.
			"--output-format", "json",
			"--tools", "", // Disable all tools
			"--disable-slash-commands", // No slash commands
			"--no-session-persistence", // Don't save session
			"--strict-mcp-config",      // ignore .mcp.json / external MCP servers
			"--system-prompt", "",      // drop CLAUDE.md/memory/identity so they don't pollute the result
			// Isolate via in-line overrides rather than `--setting-sources ""`:
			// an empty source list also drops the model config, so the CLI routes
			// generation to its built-in fast model regardless of --model. These
			// overrides disable hooks/MCP/attribution while leaving the requested
			// model in force.
			"--settings", minimalSettings(req.Model),
		)
	}

	// Interactive delivers the initial prompt as an argv positional (it's short —
	// a human typed it). Oneshot pipes the task on stdin instead (see promptStdin /
	// Execute), so a large task (a diff to review, a session to compact) can't
	// exceed the OS argv length limit — the E2BIG that broke `ctxloom weave`
	// synthesis. buildArgs omits it here for oneshot; claude -p reads it from stdin.
	if req.Mode == agent.ModeInteractive {
		if prompt := agent.GetPromptContent(req.Prompt); prompt != "" {
			args = append(args, prompt)
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
