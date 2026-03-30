package backends

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/config"
)

// Gemini implements the Backend interface for Gemini CLI.
type Gemini struct {
	BaseBackend
	lifecycle *GeminiLifecycle
	skills    *GeminiSkills
	context   *GeminiContext
	mcp       *GeminiMCPManager
	history   *GeminiSessionHistory
}

// NewGemini creates a new Gemini backend with default settings.
func NewGemini() *Gemini {
	b := &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
	}
	b.BinaryPath = "gemini"
	b.lifecycle = NewGeminiLifecycle(b)
	b.skills = &GeminiSkills{backend: b}
	b.context = NewGeminiContext(b)
	b.mcp = NewGeminiMCPManager(b)
	b.history = NewGeminiSessionHistory(b)
	return b
}

// ContextFileName returns the target file for context injection.
func (b *Gemini) ContextFileName() string {
	return "GEMINI.md"
}

// Lifecycle returns the lifecycle handler (hooks).
func (b *Gemini) Lifecycle() LifecycleHandler {
	return b.lifecycle
}

// Skills returns the skill registry (slash commands).
func (b *Gemini) Skills() SkillRegistry {
	return b.skills
}

// Context returns the context provider (file + hook).
func (b *Gemini) Context() ContextProvider {
	return b.context
}

// MCP returns the MCP server manager.
func (b *Gemini) MCP() MCPManager {
	return b.mcp
}

// History returns the session history accessor.
func (b *Gemini) History() SessionHistory {
	return b.history
}

// Setup prepares the backend for execution.
func (b *Gemini) Setup(ctx context.Context, req *SetupRequest) error {
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
func (b *Gemini) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	// Build model info
	modelName := req.Model
	if modelName == "" {
		modelName = "gemini-2.0-flash"
	}
	modelInfo := &ModelInfo{
		ModelName: modelName,
		Provider:  "google",
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
	if (req.Prompt == nil || req.Prompt.Content == "") && req.Mode == ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, env, stdout, stderr)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, env, stdout, stderr)
	}

	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// Cleanup releases resources after execution.
func (b *Gemini) Cleanup(ctx context.Context) error {
	return nil
}

// buildArgs constructs the command-line arguments.
func (b *Gemini) buildArgs(req *ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--yolo")
	}

	if prompt := GetPromptContent(req.Prompt); prompt != "" {
		args = append(args, "-i", prompt)
	}

	return args
}

