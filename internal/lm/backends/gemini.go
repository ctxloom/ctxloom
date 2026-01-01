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

// Gemini implements the Backend interface for Gemini CLI.
type Gemini struct {
	BaseBackend
	BinaryPath string
	Args       []string
	Env        map[string]string
}

// NewGemini creates a new Gemini backend with default settings.
func NewGemini() *Gemini {
	return &Gemini{
		BaseBackend: NewBaseBackend("gemini", "1.0.0"),
		BinaryPath:  "gemini",
		Args:        []string{},
		Env:         make(map[string]string),
	}
}

// Run executes Gemini with the given request.
func (b *Gemini) Run(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, *pb.ModelInfo, error) {
	opts := req.GetOptions()
	if opts == nil {
		opts = &pb.RunOptions{}
	}

	// Write context files (.scp.context.md and update GEMINI.md)
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

	// Build model info
	modelName := "gemini-2.0-flash" // default
	if opts.Model != "" {
		modelName = opts.Model
	}
	modelInfo := &pb.ModelInfo{
		ModelName: modelName,
		Provider:  "google",
	}

	// Dry run - return without executing
	if opts.DryRun {
		return 0, modelInfo, nil
	}

	// Use PTY for interactive mode (no prompt and not oneshot mode)
	if (req.Prompt == nil || req.Prompt.Content == "") && opts.Mode == pb.ExecutionMode_INTERACTIVE {
		exitCode, err := b.runInteractive(ctx, req, args, stdout, stderr)
		return exitCode, modelInfo, err
	}
	exitCode, err := b.runNonInteractive(ctx, req, args, stdout, stderr)
	return exitCode, modelInfo, err
}

// runInteractive runs Gemini in interactive mode using a PTY.
func (b *Gemini) runInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run gemini: %w", err)
	}

	return int32(result.ExitCode), nil
}

// runNonInteractive runs Gemini in non-interactive mode.
func (b *Gemini) runNonInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run gemini: %w", err)
	}

	return int32(result.ExitCode), nil
}

// buildArgs constructs the command-line arguments for gemini.
func (b *Gemini) buildArgs(req *pb.RunRequest) []string {
	// Start with configured args
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	opts := req.GetOptions()

	// Add auto-approve flag
	if opts != nil && opts.AutoApprove {
		args = append(args, "--yolo")
	}

	// Assemble context from fragments
	context := b.AssembleContext(req.Fragments)
	promptContent := b.GetPromptContent(req)

	// Build the prompt with context prepended if provided
	var prompt string
	if context != "" && promptContent != "" {
		prompt = fmt.Sprintf("Use the following context for this conversation:\n\n%s\n\n---\n\n%s", context, promptContent)
	} else if context != "" {
		prompt = fmt.Sprintf("Use the following context for this conversation:\n\n%s", context)
	} else {
		prompt = promptContent
	}

	// Add the prompt - use -i for interactive mode with context
	if prompt != "" {
		args = append(args, "-i", prompt)
	}

	return args
}
