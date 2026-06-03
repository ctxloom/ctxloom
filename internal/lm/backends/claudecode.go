package backends

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
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

// ClaudeCode implements the Backend interface for Claude Code CLI.
type ClaudeCode struct {
	BaseBackend
	lifecycle *ClaudeLifecycle
	skills    *ClaudeSkills
	context   *ClaudeContext
	mcp       *ClaudeMCPManager
	history   *ClaudeSessionHistory
}

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	b := &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
	}
	b.BinaryPath = "claude"
	b.lifecycle = NewClaudeLifecycle(b)
	b.skills = &ClaudeSkills{backend: b}
	b.context = NewClaudeContext(b)
	b.mcp = NewClaudeMCPManager(b)
	b.history = NewClaudeSessionHistory(b)
	return b
}

// Configure applies a decoded claude-code config to this backend.
func (b *ClaudeCode) Configure(cfg BackendConfig) {
	c, ok := cfg.(*ClaudeConfig)
	if !ok {
		return
	}
	if c.BinaryPath != "" {
		b.BinaryPath = c.BinaryPath
	}
	if len(c.Args) > 0 {
		b.Args = c.Args
	}
	for k, v := range c.Env {
		b.Env[k] = v
	}
}

// Lifecycle returns the lifecycle handler (hooks).
func (b *ClaudeCode) Lifecycle() LifecycleHandler {
	return b.lifecycle
}

// Skills returns the skill registry (slash commands).
func (b *ClaudeCode) Skills() SkillRegistry {
	return b.skills
}

// Context returns the context provider (file + hook).
func (b *ClaudeCode) Context() ContextProvider {
	return b.context
}

// MCP returns the MCP server manager.
func (b *ClaudeCode) MCP() MCPManager {
	return b.mcp
}

// History returns the session history accessor.
func (b *ClaudeCode) History() SessionHistory {
	return b.history
}

// Setup prepares the backend for execution.
func (b *ClaudeCode) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)

	// Provide context via the context provider
	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to provide context: %w", err)
	}

	// Write skills from prompts
	if prompts := LoadPrompts(); len(prompts) > 0 {
		if err := b.skills.RegisterFromContent(b.WorkDir(), prompts); err != nil {
			return fmt.Errorf("failed to register skills: %w", err)
		}
	}

	// Load and merge hooks from config
	cfg, err := config.Load()
	if err == nil {
		b.lifecycle.MergeConfigHooks(cfg, b.WorkDir(), b.context.GetContextHash())
	}

	// Flush hooks to settings file
	if err := b.lifecycle.Flush(b.WorkDir()); err != nil {
		return fmt.Errorf("failed to write hooks: %w", err)
	}

	return nil
}

// Execute runs the backend with the given request.
func (b *ClaudeCode) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	// Best-effort model identity. In minimal mode this is overwritten below with
	// the real id the CLI reports; otherwise it is the requested model, falling
	// back to the backend name rather than a fabricated version.
	modelName := req.Model
	if modelName == "" {
		modelName = b.Name()
	}
	modelInfo := &ModelInfo{
		ModelName: modelName,
		Provider:  "anthropic",
	}

	// Dry run
	if req.DryRun {
		return &ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	// Build args
	args := b.buildArgs(req)

	// Verbosity level 16+: show command
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	// Build env with context file path
	env := make(map[string]string)
	for k, v := range req.Env {
		env[k] = v
	}
	if b.context.GetContextFilePath() != "" {
		env[SCMContextFileEnv] = b.context.GetContextFilePath()
	}

	// Minimal oneshot runs with --output-format json: buffer the envelope,
	// emit the assistant text, and record the model the CLI actually used.
	if req.Mode == ModeOneshot && req.SkipSetup {
		var raw bytes.Buffer
		exitCode, err := b.RunNonInteractive(ctx, args, env, &raw, stderr)
		text, model, perr := parseClaudeJSONResult(raw.Bytes())
		if perr != nil {
			// Fault tolerant: hand back whatever the CLI emitted and keep the
			// best-effort model rather than dropping the result.
			_, _ = stdout.Write(raw.Bytes())
			return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
		}
		_, _ = io.WriteString(stdout, text)
		if model != "" {
			modelInfo.ModelName = model
		}
		return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
	}

	// Run based on mode
	var exitCode int32
	var err error
	if req.Mode == ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, env, stdout, stderr)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, env, stdout, stderr)
	}

	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// claudeJSONResult is the subset of the `claude --output-format json` envelope
// we consume: the assistant text and per-model token usage.
type claudeJSONResult struct {
	Result     string                      `json:"result"`
	ModelUsage map[string]claudeModelUsage `json:"modelUsage"`
}

// claudeModelUsage is the per-model usage block; InputTokens identifies which
// model actually processed the request (read the content to distill) versus
// ancillary helper models the CLI invokes for background tasks with near-zero
// input.
type claudeModelUsage struct {
	InputTokens int `json:"inputTokens"`
}

// parseClaudeJSONResult extracts the result text and the resolved model id from
// a Claude CLI JSON envelope. The model is the modelUsage key with the most
// input tokens — the one that read the content and produced the result — so
// provenance records the working model rather than helper models the CLI also
// touched. Ties break on sorted id for determinism.
func parseClaudeJSONResult(data []byte) (text, model string, err error) {
	var env claudeJSONResult
	if err := json.Unmarshal(data, &env); err != nil {
		return "", "", err
	}
	ids := make([]string, 0, len(env.ModelUsage))
	for id := range env.ModelUsage {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	best := ""
	bestTokens := -1
	for _, id := range ids {
		if env.ModelUsage[id].InputTokens > bestTokens {
			best, bestTokens = id, env.ModelUsage[id].InputTokens
		}
	}
	return env.Result, best, nil
}

// Cleanup releases resources after execution.
func (b *ClaudeCode) Cleanup(ctx context.Context) error {
	return nil
}

// buildArgs constructs the command-line arguments.
func (b *ClaudeCode) buildArgs(req *ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}

	// The model is resolved by the caller (the fast role's labeled config for
	// compression, the primary role's for coding); the backend no longer
	// substitutes a fast-model default. An empty model lets the CLI pick.
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	if req.Mode == ModeOneshot {
		args = append(args, "--print")
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
			"--setting-sources", "",    // Skip settings files
		)
	}

	if prompt := GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
