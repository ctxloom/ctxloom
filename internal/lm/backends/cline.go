package backends

import (
	"context"
	"fmt"
	"io"
	"strings"
)

// Cline implements the Backend interface for Cline CLI.
//
// DISCLAIMER: This plugin is untested and provided on a best-effort basis.
type Cline struct {
	BaseBackend
	context *CLIContextProvider
}

// NewCline creates a new Cline backend with default settings.
func NewCline() *Cline {
	b := &Cline{
		BaseBackend: NewBaseBackend("cline", "1.0.0"),
		context:     &CLIContextProvider{},
	}
	b.BinaryPath = "cline"
	return b
}

// Lifecycle returns nil - Cline doesn't support lifecycle hooks.
func (b *Cline) Lifecycle() LifecycleHandler { return nil }

// Skills returns nil - Cline doesn't support skills.
func (b *Cline) Skills() SkillRegistry { return nil }

// Context returns the context provider (CLI arg injection).
func (b *Cline) Context() ContextProvider { return b.context }

// MCP returns nil - Cline doesn't support MCP servers.
func (b *Cline) MCP() MCPManager { return nil }

// Setup prepares the backend for execution.
func (b *Cline) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if _, err := WriteContextFile(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to write context file: %w", err)
	}
	return b.context.Provide(b.WorkDir(), req.Fragments)
}

// Execute runs the backend with the given request.
func (b *Cline) Execute(ctx context.Context, req *ExecuteRequest, stdout, stderr io.Writer) (*ExecuteResult, error) {
	modelName := req.Model
	if modelName == "" {
		modelName = "cline-default"
	}
	modelInfo := &ModelInfo{ModelName: modelName, Provider: "cline"}

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
func (b *Cline) Cleanup(ctx context.Context) error { return nil }

func (b *Cline) buildArgs(req *ExecuteRequest) []string {
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	if req.AutoApprove {
		args = append(args, "-y")
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
		args = append(args, message)
	}

	return args
}
