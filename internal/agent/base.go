package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// LaunchSpec describes a process for the runtime to execute: the agent declares
// what to run (binary/args/merged-env/workdir and whether it needs a terminal),
// and ctxloom's injected Launcher runs it. Process execution is the runtime's
// concern, so it carries no os/exec or pty dependency into this engine-agnostic
// substrate.
type LaunchSpec struct {
	BinaryPath  string
	Args        []string
	Env         []string // full environment (already merged via BuildEnv)
	WorkDir     string
	Interactive bool // true → allocate a pty so the child sees a terminal
}

// WindowSize is a terminal size for pty resize, carried from the frontend (which
// owns the terminal) to the agent's pty.
type WindowSize struct {
	Rows uint16
	Cols uint16
}

// Launcher runs a LaunchSpec, wiring the frontend's terminal to the agent's pty:
// it copies stdin into the pty, streams the pty's output to stdout/stderr, and
// applies resize events. Returns the process exit code. ctxloom injects a
// pty-backed implementation (SetLauncher); see internal/lm/backends.RunLaunchSpec.
//
// Launch is owned by ctxloom (the runtime), not the agent: a single home for the
// pty owns terminal allocation, resize, and stdio wiring, shared by every agent.
// stdin/resize originate at the frontend and arrive over the bidi Run stream — so
// the frontend (possibly remote) owns the terminal, not the controller.
//
// TODO: generalize "launch" to a broader "open/connect a session" operation.
type Launcher func(ctx context.Context, spec LaunchSpec, stdin io.Reader, stdout, stderr io.Writer, resize <-chan WindowSize) (int32, error)

// BaseBackend provides common functionality for all AI backends.
// Embed this struct in concrete backend implementations.
type BaseBackend struct {
	name       string
	version    string
	BinaryPath string
	Args       []string
	Env        map[string]string
	workDir    string
	launcher   Launcher
}

// SetLauncher injects the process launcher. ctxloom sets a pty-backed launcher at
// registry construction; a backend with no launcher cannot run a local process.
func (b *BaseBackend) SetLauncher(l Launcher) {
	b.launcher = l
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

// GetBinaryPath returns the configured binary path. It satisfies the
// BinaryPathProvider contract the registry uses to resolve a backend's default
// binary without launching it.
func (b *BaseBackend) GetBinaryPath() string {
	return b.BinaryPath
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

// RunInteractive runs the backend's command in interactive mode: the injected
// launcher allocates a pty and wires the frontend's stdin/resize (from the bidi
// Run stream) into it. stdin/resize may be nil (e.g. a non-tty caller).
func (b *BaseBackend) RunInteractive(ctx context.Context, args []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer, resize <-chan WindowSize) (int32, error) {
	return b.run(ctx, args, env, true, stdin, stdout, stderr, resize)
}

// RunNonInteractive runs the backend's command without a pty (no stdin/resize).
func (b *BaseBackend) RunNonInteractive(ctx context.Context, args []string, env map[string]string, stdout, stderr io.Writer) (int32, error) {
	return b.run(ctx, args, env, false, nil, stdout, stderr, nil)
}

// run builds the LaunchSpec from the backend's state and hands it to the injected
// launcher. The substrate never execs a process itself.
func (b *BaseBackend) run(ctx context.Context, args []string, env map[string]string, interactive bool, stdin io.Reader, stdout, stderr io.Writer, resize <-chan WindowSize) (int32, error) {
	if b.launcher == nil {
		return 1, fmt.Errorf("no launcher configured for %s", b.name)
	}
	return b.launcher(ctx, LaunchSpec{
		BinaryPath:  b.BinaryPath,
		Args:        args,
		Env:         b.BuildEnv(env),
		WorkDir:     b.WorkDir(),
		Interactive: interactive,
	}, stdin, stdout, stderr, resize)
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

