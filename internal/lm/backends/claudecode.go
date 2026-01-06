package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/benjaminabbitt/scm/internal/config"
	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
	"github.com/benjaminabbitt/scm/internal/ptyrunner"
)

// ClaudeCode implements the Backend interface for Claude Code CLI.
type ClaudeCode struct {
	BaseBackend
	BinaryPath string
	Args       []string
	Env        map[string]string
}

// NewClaudeCode creates a new Claude Code backend with default settings.
func NewClaudeCode() *ClaudeCode {
	return &ClaudeCode{
		BaseBackend: NewBaseBackend("claude-code", "1.0.0"),
		BinaryPath:  "claude",
		Args:        []string{},
		Env:         make(map[string]string),
	}
}

// Run executes Claude Code with the given request.
func (b *ClaudeCode) Run(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, *pb.ModelInfo, error) {
	opts := req.GetOptions()
	if opts == nil {
		opts = &pb.RunOptions{}
	}

	// Write context files (.scm/context.md and update CLAUDE.md)
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "."
	}
	if err := WriteContextFiles(b.Name(), workDir, req.Fragments); err != nil {
		fmt.Fprintf(stderr, "warning: failed to write context files: %v\n", err)
	}

	args := b.buildArgs(req)

	// Verbosity level 16+: show command
	if opts.Verbosity >= 16 {
		fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	// Build model info - priority: request opts > config default > hardcoded fallback
	modelName := opts.Model
	if modelName == "" {
		if cfg, err := config.Load(); err == nil {
			modelName = cfg.LM.GetDefaultModel(b.Name())
		}
	}
	if modelName == "" {
		modelName = "claude-3-opus" // fallback
	}
	modelInfo := &pb.ModelInfo{
		ModelName: modelName,
		Provider:  "anthropic",
	}

	// Dry run - return without executing
	if opts.DryRun {
		return 0, modelInfo, nil
	}

	// Use PTY for interactive mode
	if opts.Mode == pb.ExecutionMode_INTERACTIVE {
		exitCode, err := b.runInteractive(ctx, req, args, stdout, stderr)
		return exitCode, modelInfo, err
	}
	exitCode, err := b.runNonInteractive(ctx, req, args, stdout, stderr)
	return exitCode, modelInfo, err
}

// runInteractive runs Claude Code in interactive mode using a PTY.
func (b *ClaudeCode) runInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)

	opts := req.GetOptions()
	if opts != nil && opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	// Set environment variables
	cmd.Env = os.Environ()
	for k, v := range b.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	if opts != nil {
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	result, err := ptyrunner.RunInteractive(ctx, cmd, stdout, stderr)
	if err != nil {
		return 1, fmt.Errorf("failed to run claude: %w", err)
	}

	return int32(result.ExitCode), nil
}

// runNonInteractive runs Claude Code in non-interactive mode.
func (b *ClaudeCode) runNonInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)

	opts := req.GetOptions()
	if opts != nil && opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	// Set environment variables
	cmd.Env = os.Environ()
	for k, v := range b.Env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}
	if opts != nil {
		for k, v := range opts.Env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	result, err := ptyrunner.RunNonInteractive(ctx, cmd, stdout, stderr)
	if err != nil {
		return 1, fmt.Errorf("failed to run claude: %w", err)
	}

	return int32(result.ExitCode), nil
}

// buildArgs constructs the command-line arguments for claude.
func (b *ClaudeCode) buildArgs(req *pb.RunRequest) []string {
	// Start with configured args
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	opts := req.GetOptions()

	// Add auto-approve flag
	if opts != nil && opts.AutoApprove {
		args = append(args, "--dangerously-skip-permissions")
	}

	// Add model selection
	if opts != nil && opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// Add --print for oneshot mode
	if opts != nil && opts.Mode == pb.ExecutionMode_ONESHOT {
		args = append(args, "--print")
	}

	// Assemble context from fragments and pass via --append-system-prompt
	if ctx := b.AssembleContext(req.Fragments); ctx != "" {
		args = append(args, "--append-system-prompt", ctx)
	}

	// Add the prompt as the final argument
	if prompt := b.GetPromptContent(req); prompt != "" {
		args = append(args, prompt)
	}

	return args
}
