package backends

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Aider implements the Backend interface for Aider CLI.
//
// DISCLAIMER: This plugin is untested and provided on a best-effort basis.
type Aider struct {
	BaseBackend
	context *CLIContextProvider
}

// NewAider creates a new Aider backend with default settings.
func NewAider() *Aider {
	b := &Aider{
		BaseBackend: NewBaseBackend("aider", "1.0.0"),
		context:     &CLIContextProvider{},
	}
	b.BinaryPath = "aider"
	return b
}

// Lifecycle returns nil - Aider doesn't support lifecycle hooks.
func (b *Aider) Lifecycle() LifecycleHandler { return nil }

// Skills returns nil - Aider doesn't support skills.
func (b *Aider) Skills() SkillRegistry { return nil }

// Context returns the context provider (CLI arg injection).
func (b *Aider) Context() ContextProvider { return b.context }

// MCP returns nil - Aider doesn't support MCP servers.
func (b *Aider) MCP() MCPManager { return nil }

// Setup prepares the backend for execution.
func (b *Aider) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if _, err := WriteContextFile(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to write context file: %w", err)
	}
	return b.context.Provide(b.WorkDir(), req.Fragments)
}

// Execute runs the backend with the given request.
func (b *Aider) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = "gpt-4"
	}
	modelInfo := &ModelInfo{ModelName: modelName, Provider: "aider"}

	if req.DryRun {
		return &ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}

	args := b.buildArgs(req)
	if req.Verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	var exitCode int32
	var err error
	if req.Mode == ModeInteractive {
		exitCode, err = b.RunInteractive(ctx, args, req.Env, stdout, stderr)
	} else {
		exitCode, err = b.RunNonInteractive(ctx, args, req.Env, stdout, stderr)
	}

	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// Cleanup releases resources after execution.
func (b *Aider) Cleanup(ctx context.Context) error { return nil }

func (b *Aider) buildArgs(req *ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "--yes-always")
	}
	if req.Model != "" {
		args = append(args, "--model", req.Model)
	}
	if req.Temperature > 0 {
		args = append(args, "--temperature", fmt.Sprintf("%.2f", req.Temperature))
	}

	context := b.context.GetAssembled()
	prompt := GetPromptContent(req.Prompt)
	if prompt != "" {
		var message string
		if context != "" {
			message = fmt.Sprintf("Context:\n%s\n\n---\n\nTask: %s", context, prompt)
		} else {
			message = prompt
		}
		args = append(args, "--message", message)
	}

	return args
}
