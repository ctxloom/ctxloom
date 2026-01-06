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

// Goose implements the Backend interface for Goose AI CLI (Block's coding assistant).
//
// DISCLAIMER: This plugin is untested and provided on a best-effort basis.
// Bug reports are welcome. If the Block/Goose team would like to provide API credits
// or licenses for testing, contributions to improve this integration are appreciated.
type Goose struct {
	BaseBackend
	BinaryPath string
	Args       []string
	Env        map[string]string
}

// NewGoose creates a new Goose backend with default settings.
func NewGoose() *Goose {
	return &Goose{
		BaseBackend: NewBaseBackend("goose", "1.0.0"),
		BinaryPath:  "goose",
		Args:        []string{},
		Env:         make(map[string]string),
	}
}

// Run executes Goose with the given request.
func (b *Goose) Run(ctx context.Context, req *pb.RunRequest, stdout, stderr io.Writer) (int32, *pb.ModelInfo, error) {
	opts := req.GetOptions()
	if opts == nil {
		opts = &pb.RunOptions{}
	}

	// Write context files (.scm/context.md and update GOOSE.md)
	workDir := opts.WorkDir
	if workDir == "" {
		workDir = "."
	}
	if err := WriteContextFiles(b.Name(), workDir, req.Fragments); err != nil {
		fmt.Fprintf(stderr, "warning: failed to write context files: %v\n", err)
	}

	args := b.buildArgs(req)

	// Verbosity level 16+: show command (optional, defaults to 0)
	if opts.Verbosity >= 16 {
		fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}

	// Build model info (optional model override)
	modelName := "goose-default"
	if opts.Model != "" {
		modelName = opts.Model
	}
	modelInfo := &pb.ModelInfo{
		ModelName: modelName,
		Provider:  "block",
	}

	// Dry run - return without executing (optional, defaults to false)
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

// runInteractive runs Goose in interactive mode using a PTY.
func (b *Goose) runInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run goose: %w", err)
	}

	return int32(result.ExitCode), nil
}

// runNonInteractive runs Goose in non-interactive mode.
func (b *Goose) runNonInteractive(ctx context.Context, req *pb.RunRequest, args []string, stdout, stderr io.Writer) (int32, error) {
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
		return 1, fmt.Errorf("failed to run goose: %w", err)
	}

	return int32(result.ExitCode), nil
}

// buildArgs constructs the command-line arguments for goose.
func (b *Goose) buildArgs(req *pb.RunRequest) []string {
	// Start with configured args
	args := make([]string, len(b.Args))
	copy(args, b.Args)

	// Assemble context from fragments
	context := b.AssembleContext(req.Fragments)
	promptContent := b.GetPromptContent(req)

	// Goose uses "session" subcommand for interactive mode
	// For now, pass context and prompt together
	if promptContent != "" {
		var prompt string
		if context != "" {
			prompt = fmt.Sprintf("Context:\n%s\n\n---\n\nTask: %s", context, promptContent)
		} else {
			prompt = promptContent
		}
		// Use --message or similar flag depending on goose CLI
		args = append(args, prompt)
	}

	return args
}
