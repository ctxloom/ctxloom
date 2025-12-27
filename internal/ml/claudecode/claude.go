package claudecode

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/benjaminabbitt/mlcm/internal/ml"
	"github.com/benjaminabbitt/mlcm/internal/ptyrunner"
)

const pluginName = "claude-code"

// Plugin implements the ml.Plugin and ml.ConfigurablePlugin interfaces for Claude Code CLI.
type Plugin struct {
	binaryPath string
	args       []string
	env        map[string]string
}

// New creates a new Claude Code plugin with default settings.
func New() *Plugin {
	return &Plugin{
		binaryPath: "claude",
		args:       []string{},
		env:        make(map[string]string),
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return pluginName
}

// Clone returns a new instance of this plugin with the same base configuration.
func (p *Plugin) Clone() ml.Plugin {
	clone := &Plugin{
		binaryPath: p.binaryPath,
		args:       make([]string, len(p.args)),
		env:        make(map[string]string, len(p.env)),
	}
	copy(clone.args, p.args)
	for k, v := range p.env {
		clone.env[k] = v
	}
	return clone
}

// Configure applies the given configuration to the plugin.
func (p *Plugin) Configure(cfg ml.PluginConfig) {
	if cfg.BinaryPath != "" {
		p.binaryPath = cfg.BinaryPath
	}
	if len(cfg.Args) > 0 {
		p.args = cfg.Args
	}
	if cfg.Env != nil {
		p.env = cfg.Env
	}
}

// Run executes Claude Code with the given request.
func (p *Plugin) Run(ctx context.Context, req ml.Request, stdout, stderr io.Writer) (*ml.Response, error) {
	// Use PTY for interactive mode unless --print is requested
	// Claude supports initial prompts while staying interactive
	if !req.Print {
		return p.runInteractive(ctx, req, stdout, stderr)
	}
	return p.runNonInteractive(ctx, req, stdout, stderr)
}

// runInteractive runs Claude Code in interactive mode using a PTY.
func (p *Plugin) runInteractive(ctx context.Context, req ml.Request, stdout, stderr io.Writer) (*ml.Response, error) {
	args := p.buildArgs(req)
	cmd := exec.CommandContext(ctx, p.binaryPath, args...)

	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}

	// Set environment variables
	cmd.Env = os.Environ()
	for k, v := range p.env {
		cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
	}

	result, err := ptyrunner.RunInteractive(ctx, cmd, stdout, stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to run claude: %w", err)
	}

	return &ml.Response{
		Output:   result.Output,
		ExitCode: result.ExitCode,
	}, nil
}

// runNonInteractive runs Claude Code in non-interactive mode.
func (p *Plugin) runNonInteractive(ctx context.Context, req ml.Request, stdout, stderr io.Writer) (*ml.Response, error) {
	args := p.buildArgs(req)
	cmd := exec.CommandContext(ctx, p.binaryPath, args...)

	if req.WorkDir != "" {
		cmd.Dir = req.WorkDir
	}

	// Set environment variables
	if len(p.env) > 0 {
		cmd.Env = os.Environ()
		for k, v := range p.env {
			cmd.Env = append(cmd.Env, fmt.Sprintf("%s=%s", k, v))
		}
	}

	result, err := ptyrunner.RunNonInteractive(ctx, cmd, stdout, stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to run claude: %w", err)
	}

	return &ml.Response{
		Output:   result.Output,
		ExitCode: result.ExitCode,
	}, nil
}

// buildArgs constructs the command-line arguments for claude.
func (p *Plugin) buildArgs(req ml.Request) []string {
	// Start with configured args
	args := make([]string, len(p.args))
	copy(args, p.args)

	// Add --print for non-interactive mode
	if req.Print {
		args = append(args, "--print")
	}

	// Add context via --append-system-prompt if provided
	if req.Context != "" {
		args = append(args, "--append-system-prompt", req.Context)
	}

	// Add the prompt as the final argument
	if req.Prompt != "" {
		args = append(args, req.Prompt)
	}

	return args
}

// CommandPreview returns the command that would be executed for the given request.
func (p *Plugin) CommandPreview(req ml.Request) string {
	args := p.buildArgs(req)

	// Quote arguments that contain spaces or special characters
	quotedArgs := make([]string, len(args))
	for i, arg := range args {
		if strings.ContainsAny(arg, " \t\n\"'") {
			quotedArgs[i] = fmt.Sprintf("%q", arg)
		} else {
			quotedArgs[i] = arg
		}
	}

	return p.binaryPath + " " + strings.Join(quotedArgs, " ")
}

func init() {
	ml.Register(New())
}
