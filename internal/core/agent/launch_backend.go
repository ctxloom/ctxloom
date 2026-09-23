package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/sessions"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// SessionHarpEnv is sessions.EnvHarp under this package's established name:
// the env var carrying ctxloom's per-session harp name. The host sets it on
// the run env; an engine's Execute reads it to name its session.
const SessionHarpEnv = sessions.EnvHarp

// ManagedLifecycle folds a host-assembled ManagedConfig into its managed hooks +
// MCP; the at-rest writers read the merged state (GetHooks/GetMCP) to write
// each settings/config surface. BaseLifecycle implements it.
type ManagedLifecycle interface {
	MergeManaged(rep report.Reporter, m *ManagedConfig, workDir, contextHash string)
}

// HashedContext is a ContextProvider that exposes the content hash and on-disk
// path of the context it last provided. BaseContextProvider implements it; the
// hash seeds the agent's context-injection hook and the path is handed to the
// child process via the SCM context-file env var.
type HashedContext interface {
	ContextProvider
	GetContextHash() string
	GetContextFilePath() string
}

// LaunchBackend is the shared core of a local-CLI launch agent (claude).
// It owns the capability wiring (lifecycle/commands/context/history) and the
// generic Execute tail every launch agent shares. A concrete agent embeds
// it, calls InitLaunch with its constructed capabilities, and implements only
// the genuinely engine-specific surface: Configure, Execute, and its config's
// BackendType. Delivery is not this type's: the runner delivers the launch's
// package through the static writer before Execute runs.
type LaunchBackend struct {
	BaseBackend
	lifecycle ManagedLifecycle
	context   HashedContext
	history   SessionHistory

	// surfaces is the engine's static Declaration: which approaches it
	// constructs for each surface kind — the engine's own account of its
	// surfaces, read by the tooling that reports them.
	surfaces Declaration
	// extraEnv, when set, contributes per-backend child-env entries on top of the
	// shared ExecuteEnv (the request env + the SCM context-file path) — the seam a
	// backend uses to add env computed from the request without reimplementing
	// the shared assembly.
	extraEnv func(req *ExecuteRequest) map[string]string
	// engineHomeVar names the env var that relocates this engine's config
	// home (claude's CLAUDE_CONFIG_DIR). Empty for an engine that never
	// declared one: the root is a fact the engine states, never inferred
	// from the environment.
	engineHomeVar string
}

// InitLaunch wires the constructed capabilities into the base. Call it from the
// concrete constructor once the capabilities (which usually close over the
// concrete backend) have been built. surfaces is the engine's Declaration of
// the approaches it delivers at launch.
func (b *LaunchBackend) InitLaunch(lifecycle ManagedLifecycle, ctxProvider HashedContext, history SessionHistory, surfaces Declaration) {
	b.lifecycle = lifecycle
	b.context = ctxProvider
	b.history = history
	b.surfaces = surfaces
}

// SetExecuteEnv registers a per-backend child-env contributor merged into
// ExecuteEnv. A backend uses it to inject env computed from the ExecuteRequest
// (claude's exec env), without reimplementing the shared env assembly. Later entries win over the shared ones on a key clash.
func (b *LaunchBackend) SetExecuteEnv(fn func(req *ExecuteRequest) map[string]string) {
	b.extraEnv = fn
}

// SetEngineHomeVar names the env var that relocates this engine's config
// home, so a run that carries it (an agent binding with engine_home: session)
// advises its private engine home to every writer. See engineHomeVar.
func (b *LaunchBackend) SetEngineHomeVar(name string) { b.engineHomeVar = name }

// History returns the session history accessor.
func (b *LaunchBackend) History() SessionHistory { return b.history }

// ExecuteCLI runs the shared tail of an exec-style Execute: the dry-run
// preview stop, the v16 argv trace, env assembly (the request env plus the
// SCM context-file path), and interactive/non-interactive routing. A concrete
// backend resolves its model + argv — the genuinely engine-specific half —
// and delegates the launch here, so the launch plumbing can't drift between
// engines.
// oneshotStdin, when non-nil, is fed to the child's stdin for a non-interactive
// run — the channel a backend uses to deliver a large oneshot prompt off the
// argv (which the OS length-limits). It is ignored for an interactive run, whose
// stdin is the frontend's (req.Stdin).
func (b *LaunchBackend) ExecuteCLI(ctx context.Context, req *ExecuteRequest, args []string, oneshotStdin io.Reader, modelInfo *ModelInfo, stdout, stderr io.Writer) (*ExecuteResult, error) {
	if req.DryRun {
		return &ExecuteResult{ExitCode: 0, ModelInfo: modelInfo}, nil
	}
	// Refuse an argv the OS cannot exec BEFORE trying, so the failure names
	// the payload rather than arriving as os/exec's generic "argument list too
	// long" — which points at the total argument list, the innocent part. This
	// lives here, once, because every exec-style backend funnels its launch
	// through this tail, so an engine that carries the prompt on argv
	// (claude's interactive arm) is covered without repeating the check. See
	// argvlimit.go.
	if err := checkArgvLimit(b.Name(), args, GetPromptContent(req.Prompt),
		singleArgLimit(perArgCapped, os.Getpagesize())); err != nil {
		return nil, err
	}
	b.TraceArgs(req.Verbosity, args, stderr)
	env := b.ExecuteEnv(req)
	if req.Mode == ModeInteractive {
		exitCode, err := b.RunInteractive(ctx, args, env, req.Stdin, req.StdinCleanup, stdout, stderr, req.Resize)
		return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
	}
	exitCode, err := b.RunNonInteractive(ctx, args, env, oneshotStdin, stdout, stderr)
	return &ExecuteResult{ExitCode: exitCode, ModelInfo: modelInfo}, err
}

// TraceArgs prints the resolved argv at verbosity 16+ — the launch trace
// every exec-style backend shows.
func (b *LaunchBackend) TraceArgs(verbosity uint32, args []string, stderr io.Writer) {
	if verbosity >= 16 {
		_, _ = fmt.Fprintf(stderr, "[v16] %s %s\n", b.BinaryPath, strings.Join(args, " "))
	}
}

// ExecuteEnv assembles the child env: the request env, the SCM context-file path
// when context was provided, and any per-backend contributor (SetExecuteEnv).
func (b *LaunchBackend) ExecuteEnv(req *ExecuteRequest) map[string]string {
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	if p := b.contextFilePath(); p != "" {
		env[SCMContextFileEnv] = p
	}
	if b.extraEnv != nil {
		for k, v := range b.extraEnv(req) {
			env[k] = v
		}
	}
	return env
}

// contextFilePath returns the on-disk path of the provided context file, or ""
// when no context was provided. ExecuteEnv passes it into the child env via
// the SCM context-file variable. Unexported: its only caller is in this file.
func (b *LaunchBackend) contextFilePath() string {
	if b.context == nil {
		return ""
	}
	return b.context.GetContextFilePath()
}

// Cleanup implements Backend. The runner owns what it delivered for the
// launch (its static writer's ownership record), so there is nothing here
// to unwind.
func (b *LaunchBackend) Cleanup(context.Context) error { return nil }
