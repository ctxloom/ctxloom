package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	pb "github.com/benjaminabbitt/scm/internal/lm/grpc"
	"github.com/benjaminabbitt/scm/internal/ptyrunner"
)

// Aider implements the Backend interface for Aider CLI.
//
// DISCLAIMER: This plugin is untested and provided on a best-effort basis.
// Bug reports are welcome. If the Aider team would like to provide API credits
// or licenses for testing, contributions to improve this integration are appreciated.
type Aider struct {
	BaseBackend
	BinaryPath string
	Args       []string
	Env        map[string]string
}

// NewAider creates a new Aider backend with default settings.
func NewAider() *Aider {
	return &Aider{
		BaseBackend: NewBaseBackend("aider", "1.0.0"),
		BinaryPath:  "aider",
		Args:        []string{},
		Env:         make(map[string]string),
	}
}

// Run executes Aider with the given request.
func (b *Aider) Run(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, *pb.ModelInfo, error) {
	opts := req.GetOptions()
	if opts == nil {
		opts = &pb.RunOptions{}
	}

	// Write context files (.scp.context.md and update AIDER.md)
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

	// Build model info (aider uses various models)
	modelName := "gpt-4" // aider default
	if opts.Model != "" {
		modelName = opts.Model
	}
	modelInfo := &pb.ModelInfo{
		ModelName: modelName,
		Provider:  "aider",
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

// runInteractive runs Aider in interactive mode using a PTY.
func (b *Aider) runInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run aider: %w", err)
	}

	return int32(result.ExitCode), nil
}

// runNonInteractive runs Aider in non-interactive mode.
func (b *Aider) runNonInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run aider: %w", err)
	}

	return int32(result.ExitCode), nil
}

// buildArgs constructs the command-line arguments for aider.
func (b *Aider) buildArgs(req *pb.RunRequest) []string {
	// Start with configured args
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	opts := req.GetOptions()

	// Add auto-approve flag if configured
	if opts != nil && opts.AutoApprove {
		args = append(args, "--yes-always")
	}

	// Add model selection
	if opts != nil && opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}

	// Add temperature if set
	if opts != nil && opts.Temperature > 0 {
		args = append(args, "--temperature", fmt.Sprintf("%.2f", opts.Temperature))
	}

	// Assemble context from fragments and build message
	context := b.AssembleContext(req.Fragments)
	promptContent := b.GetPromptContent(req)

	// Aider uses --message for the initial prompt
	if promptContent != "" {
		var message string
		if context != "" {
			message = fmt.Sprintf("Context:\n%s\n\n---\n\nTask: %s", context, promptContent)
		} else {
			message = promptContent
		}
		args = append(args, "--message", message)
	}

	return args
}
