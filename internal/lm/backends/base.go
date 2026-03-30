package backends

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/SophisticatedContextManager/scm/internal/bundles"
	"github.com/SophisticatedContextManager/scm/internal/config"
	"github.com/SophisticatedContextManager/scm/internal/ptyrunner"
)

// BaseBackend provides common functionality for all AI backends.
// Embed this struct in concrete backend implementations.
type BaseBackend struct {
	name       string
	version    string
	BinaryPath string
	Args       []string
	Env        map[string]string
	workDir    string
}

// NewBaseBackend creates a new BaseBackend with the given name and version.
func NewBaseBackend(name, version string) BaseBackend {
	return BaseBackend{
		name:    name,
		version: version,
		Args:    []string{},
		Env:     make(map[string]string),
	}
}

// Name returns the backend identifier.
func (b *BaseBackend) Name() string {
	return b.name
}

// Version returns the backend version.
func (b *BaseBackend) Version() string {
	return b.version
}

// SupportedModes returns the default supported modes (both interactive and oneshot).
func (b *BaseBackend) SupportedModes() []ExecutionMode {
	return []ExecutionMode{ModeInteractive, ModeOneshot}
}

// WorkDir returns the current working directory.
func (b *BaseBackend) WorkDir() string {
	if b.workDir == "" {
		return "."
	}
	return b.workDir
}

// SetWorkDir sets the working directory.
func (b *BaseBackend) SetWorkDir(dir string) {
	b.workDir = dir
}

// BuildEnv constructs environment variables from backend and request.
func (b *BaseBackend) BuildEnv(reqEnv map[string]string) []string {
	env := os.Environ()
	for k, v := range b.Env {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	for k, v := range reqEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// RunInteractive runs a command in interactive mode using a PTY.
// The PTY ensures the child process sees a terminal, enabling interactive CLI tools
// to work correctly even when stdin is a pipe (e.g., from go-plugin gRPC).
func (b *BaseBackend) RunInteractive(ctx context.Context, args []string, env map[string]string, stdout, stderr interface{ Write([]byte) (int, error) }) (int32, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)
	cmd.Dir = b.WorkDir()
	cmd.Env = b.BuildEnv(env)

	result, err := ptyrunner.RunInteractive(ctx, cmd, stdout, stderr)
	if err != nil {
		return 1, fmt.Errorf("failed to run %s: %w", b.name, err)
	}

	return int32(result.ExitCode), nil
}

// RunNonInteractive runs a command in non-interactive mode.
func (b *BaseBackend) RunNonInteractive(ctx context.Context, args []string, env map[string]string, stdout, stderr interface{ Write([]byte) (int, error) }) (int32, error) {
	cmd := exec.CommandContext(ctx, b.BinaryPath, args...)
	cmd.Dir = b.WorkDir()
	cmd.Env = b.BuildEnv(env)

	// Don't attach stdin - non-interactive mode should not wait for input
	cmd.Stdin = nil
	if stdout != nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, stdout)
	} else {
		cmd.Stdout = os.Stdout
	}
	if stderr != nil {
		cmd.Stderr = io.MultiWriter(os.Stderr, stderr)
	} else {
		cmd.Stderr = os.Stderr
	}

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return int32(exitErr.ExitCode()), nil
		}
		return 1, fmt.Errorf("failed to run %s: %w", b.name, err)
	}

	return 0, nil
}

// AssembleContext combines fragments into a single context string.
func AssembleContext(fragments []*Fragment) string {
	if len(fragments) == 0 {
		return ""
	}

	var parts []string
	for _, f := range fragments {
		if f.Content == "" {
			continue
		}
		parts = append(parts, strings.TrimSpace(f.Content))
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// GetPromptContent extracts prompt content from a fragment.
func GetPromptContent(prompt *Fragment) string {
	if prompt != nil {
		return prompt.Content
	}
	return ""
}

// LoadPrompts loads all prompts from bundles for slash command export.
// This is shared logic used by multiple backends.
func LoadPrompts() []*bundles.LoadedContent {
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
