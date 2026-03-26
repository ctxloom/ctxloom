package backends

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
)

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
	b.lifecycle = &ClaudeLifecycle{backend: b}
	b.skills = &ClaudeSkills{backend: b}
	b.context = &ClaudeContext{backend: b}
	b.mcp = &ClaudeMCPManager{backend: b}
	b.history = &ClaudeSessionHistory{backend: b}
	return b
}

// Configure applies plugin configuration to this backend.
func (b *ClaudeCode) Configure(cfg *config.PluginConfig) {
	if cfg.BinaryPath != "" {
		b.BinaryPath = cfg.BinaryPath
	}
	if len(cfg.Args) > 0 {
		b.Args = cfg.Args
	}
	for k, v := range cfg.Env {
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
	if prompts := b.loadPrompts(); len(prompts) > 0 {
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
	// Build model info
	modelName := req.Model
	if modelName == "" {
		if cfg, err := config.Load(); err == nil {
			modelName = cfg.LM.GetDefaultModel(b.Name())
		}
	}
	if modelName == "" {
		modelName = "claude-3-opus"
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

	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}

	if req.Mode == ModeOneshot {
		args = append(args, "--print")
	}

	if prompt := GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, prompt)
	}

	return args
}

// loadPrompts loads all prompts from bundles for slash command export.
func (b *ClaudeCode) loadPrompts() []*bundles.LoadedContent {
	cfg, err := config.Load()
	if err != nil {
		return nil
	}

	bundleDirs := cfg.GetBundleDirs()
	if len(bundleDirs) == 0 {
		return nil
	}

	loader := bundles.NewLoader(bundleDirs, cfg.Defaults.ShouldUseDistilled())
	infos, err := loader.ListAllPrompts()
	if err != nil {
		return nil
	}

	var prompts []*bundles.LoadedContent
	for _, info := range infos {
		content, err := loader.GetPrompt(info.Name)
		if err != nil {
			continue
		}
		prompts = append(prompts, content)
	}

	return prompts
}
