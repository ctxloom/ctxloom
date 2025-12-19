package gemini

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"

	"mlcm/internal/ai"
)

const pluginName = "gemini"

// Plugin implements the ai.Plugin and ai.ConfigurablePlugin interfaces for Gemini CLI.
type Plugin struct {
	binaryPath string
	args       []string
	env        map[string]string
}

// New creates a new Gemini plugin with default settings.
func New() *Plugin {
	return &Plugin{
		binaryPath: "gemini",
		args:       []string{},
		env:        make(map[string]string),
	}
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string {
	return pluginName
}

// Configure applies the given configuration to the plugin.
func (p *Plugin) Configure(cfg ai.PluginConfig) {
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

// Run executes Gemini with the given request.
func (p *Plugin) Run(ctx context.Context, req ai.Request, stdout, stderr io.Writer) (*ai.Response, error) {
	// Use PTY for interactive mode (no prompt and not print mode)
	if req.Prompt == "" && !req.Print {
		return p.runInteractive(ctx, req, stdout, stderr)
	}
	return p.runNonInteractive(ctx, req, stdout, stderr)
}

// runInteractive runs Gemini in interactive mode using a PTY.
func (p *Plugin) runInteractive(ctx context.Context, req ai.Request, stdout, stderr io.Writer) (*ai.Response, error) {
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

	// Start command with a PTY
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start gemini with pty: %w", err)
	}
	defer ptmx.Close()

	// Handle terminal resize
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)
	go func() {
		for range ch {
			if err := pty.InheritSize(os.Stdin, ptmx); err != nil {
				// Ignore resize errors
			}
		}
	}()
	ch <- syscall.SIGWINCH // Initial resize
	defer func() { signal.Stop(ch); close(ch) }()

	// Set stdin to raw mode if it's a terminal
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
		if err == nil {
			defer term.Restore(int(os.Stdin.Fd()), oldState)
		}
	}

	// Copy stdin to PTY
	go func() {
		io.Copy(ptmx, os.Stdin)
	}()

	// Copy PTY output to stdout
	var stdoutBuf bytes.Buffer
	if stdout != nil {
		io.Copy(io.MultiWriter(stdout, &stdoutBuf), ptmx)
	} else {
		io.Copy(&stdoutBuf, ptmx)
	}

	// Wait for command to finish
	err = cmd.Wait()

	resp := &ai.Response{
		Output:   stdoutBuf.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			// PTY close errors after command exit are normal
			if !strings.Contains(err.Error(), "input/output error") {
				return nil, fmt.Errorf("failed to run gemini: %w", err)
			}
		}
	}

	return resp, nil
}

// runNonInteractive runs Gemini in non-interactive mode.
func (p *Plugin) runNonInteractive(ctx context.Context, req ai.Request, stdout, stderr io.Writer) (*ai.Response, error) {
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

	// Connect stdin for cases where we have a prompt but might need input
	cmd.Stdin = os.Stdin

	var stdoutBuf, stderrBuf bytes.Buffer
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(stdout, &stdoutBuf)
	} else {
		cmd.Stdout = &stdoutBuf
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(stderr, &stderrBuf)
	} else {
		cmd.Stderr = &stderrBuf
	}

	err := cmd.Run()

	resp := &ai.Response{
		Output:   stdoutBuf.String(),
		ExitCode: 0,
	}

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			resp.ExitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("failed to run gemini: %w", err)
		}
	}

	return resp, nil
}

// buildArgs constructs the command-line arguments for gemini.
func (p *Plugin) buildArgs(req ai.Request) []string {
	// Start with configured args
	args := make([]string, len(p.args))
	copy(args, p.args)

	// Build the prompt with context prepended if provided
	var prompt string
	if req.Context != "" && req.Prompt != "" {
		prompt = fmt.Sprintf("Use the following context for this conversation:\n\n%s\n\n---\n\n%s", req.Context, req.Prompt)
	} else if req.Context != "" {
		prompt = fmt.Sprintf("Use the following context for this conversation:\n\n%s", req.Context)
	} else {
		prompt = req.Prompt
	}

	// Add the prompt - use -i for interactive mode with context
	if prompt != "" {
		// Use --prompt-interactive to start with context and stay interactive
		args = append(args, "-i", prompt)
	}

	return args
}

// CommandPreview returns the command that would be executed for the given request.
func (p *Plugin) CommandPreview(req ai.Request) string {
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
	ai.Register(New())
}
