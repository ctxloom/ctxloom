package agent

import (
	"context"
	"fmt"
	"io"
	"os"
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
	Interactive bool // true → the engine runs on Terminal

	// Terminal is what an interactive launch runs the engine on (see
	// Terminal); nil for a launch over pipes. The launcher refuses an
	// interactive spec without one rather than start an engine with nothing
	// to draw on.
	Terminal *Terminal
}

// Terminal is the terminal an interactive engine runs on: the three standard
// streams it inherits as they are, with no pty and no relay of the launcher's
// own between it and the human. On Unix all three are the one tty the runner
// was started on (the pty slave the originator holds the master of, or a
// container's -it tty); on Windows, the console's input and output handles.
// Its window size and resizes are the terminal's own, read by the engine
// itself.
type Terminal struct {
	Stdin, Stdout, Stderr *os.File
}

// WindowSize is a terminal size, as the frontend reads it off the human's
// terminal and applies it to the pty the engine is on.
type WindowSize struct {
	Rows uint16
	Cols uint16
}

// Launcher runs a LaunchSpec and returns the process exit code. An interactive
// spec's engine inherits spec.Terminal as its stdio; a non-interactive one
// reads stdin and writes stdout and stderr, which an interactive launch never
// touches. ctxloom injects the runner's implementation (SetLauncher); see
// runner.RunLaunchSpec.
//
// Launch is owned by ctxloom (the runtime), not the agent: one home for
// process execution and stdio wiring, shared by every agent.
//
// TODO: generalize "launch" to a broader "open/connect a session" operation.
type Launcher func(ctx context.Context, spec LaunchSpec, stdin io.Reader, stdout, stderr io.Writer) (int32, error)

// BaseBackend provides common functionality for all AI backends.
// Embed this struct in concrete backend implementations.
type BaseBackend struct {
	name     string
	version  string
	workDir  string
	launcher Launcher
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

// BuildEnv constructs the launched process's environment: the ambient
// environment (which is how an engine's own credentials reach it — ctxloom
// carries none) plus the request's own entries.
func (b *BaseBackend) BuildEnv(reqEnv map[string]string) []string {
	env := os.Environ()
	for k, v := range reqEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	return env
}

// RunInteractive runs binary in interactive mode on terminal: the injected
// launcher hands the engine the terminal as its stdio, and refuses a nil one.
func (b *BaseBackend) RunInteractive(ctx context.Context, binary string, args []string, env map[string]string, terminal *Terminal) (int32, error) {
	return b.run(ctx, binary, args, env, LaunchSpec{Interactive: true, Terminal: terminal}, nil, io.Discard, io.Discard)
}

// RunNonInteractive runs binary over pipes. stdin is the child's standard
// input — nil for a run that takes no input, or a finite reader to deliver a
// oneshot prompt off the argv (which the OS length-limits).
func (b *BaseBackend) RunNonInteractive(ctx context.Context, binary string, args []string, env map[string]string, stdin io.Reader, stdout, stderr io.Writer) (int32, error) {
	return b.run(ctx, binary, args, env, LaunchSpec{}, stdin, stdout, stderr)
}

// run completes mode — the spec's Interactive and Terminal — with binary and
// the backend's state and hands it to the injected launcher. The substrate
// never execs a process itself.
func (b *BaseBackend) run(ctx context.Context, binary string, args []string, env map[string]string, mode LaunchSpec, stdin io.Reader, stdout, stderr io.Writer) (int32, error) {
	if b.launcher == nil {
		return 1, fmt.Errorf("no launcher configured for %s", b.name)
	}
	mode.BinaryPath = binary
	mode.Args = args
	mode.Env = b.BuildEnv(env)
	mode.WorkDir = b.WorkDir()
	return b.launcher(ctx, mode, stdin, stdout, stderr)
}

// AssembleContext combines fragments into the single context string ctxloom
// delivers.
//
// It is THE assembler. There is exactly one, and every path that needs "the
// assembled context" goes through it: WriteContextFile's full-setup delivery
// and the launch surfaces. That is the point — a second implementation would
// produce different bytes for the same fragment set depending on the route,
// and a warning only one route emits lets a run deliver an oversize context in
// silence by choosing the other. A warning a caller can skip by picking a
// different function is not a warning.
//
// The work itself lives in assembleDedupedContext (contextfile.go), beside the
// dedup identity rule and the oversize threshold it enforces; this is the
// exported name shared code calls. Use WithContextStderr to redirect the
// oversize warning in tests.
func AssembleContext(fragments []*Fragment, opts ...ContextFileOption) string {
	return assembleDedupedContext(fragments, opts...)
}

// GetPromptContent extracts prompt content from a fragment.
func GetPromptContent(prompt *Fragment) string {
	if prompt != nil {
		return prompt.Content
	}
	return ""
}
