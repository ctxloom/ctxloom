package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

// ManagedLifecycle folds a host-assembled ManagedConfig into its managed hooks
// and flushes them to the agent's settings file. BaseLifecycle implements it.
type ManagedLifecycle interface {
	MergeManaged(m *ManagedConfig, workDir, contextHash string)
	Flush(workDir string) error
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

// ContentSkills registers host-resolved command exports (slash commands)
// directly. The host maps bundle content to engine-agnostic CommandExports and
// the agent writes them in its native command format.
type ContentSkills interface {
	RegisterFromContent(workDir string, cmds []CommandExport) error
}

// LaunchBackend is the shared core of a local-CLI launch agent (claude/antigravity).
// It owns the capability wiring (lifecycle/skills/context/history) and the
// generic Setup/Cleanup that every launch agent shares. A concrete agent embeds
// it, calls InitLaunch with its constructed capabilities, and implements only
// the genuinely engine-specific surface: Configure, Execute, and its config's
// BackendType.
type LaunchBackend struct {
	BaseBackend
	lifecycle ManagedLifecycle
	skills    ContentSkills
	context   HashedContext
	history   SessionHistory

	// nativeContextDelivery marks a backend (claude) that loads ctxloom's
	// assembled context from a launch flag (--append-system-prompt-file) rather
	// than the SessionStart injection hook. When set, Setup materializes the
	// framed context file and omits the context-injection hook.
	nativeContextDelivery bool
	// nativeContextPath is the framed context file Setup materialized for
	// launch-flag delivery, read by the concrete backend's buildArgs.
	nativeContextPath string
}

// EnableNativeContextDelivery marks this backend as delivering ctxloom's
// assembled context via a launch flag (claude's --append-system-prompt-file)
// instead of the SessionStart context-injection hook. Call from the concrete
// constructor; Setup then materializes the framed file and suppresses the hook.
func (b *LaunchBackend) EnableNativeContextDelivery() { b.nativeContextDelivery = true }

// NativeContextFilePath returns the framed context file Setup materialized for
// launch-flag delivery, or "" (native delivery off, or no context this run).
func (b *LaunchBackend) NativeContextFilePath() string { return b.nativeContextPath }

// InitLaunch wires the constructed capabilities into the base. Call it from the
// concrete constructor once the capabilities (which usually close over the
// concrete backend) have been built.
func (b *LaunchBackend) InitLaunch(lifecycle ManagedLifecycle, skills ContentSkills, ctxProvider HashedContext, history SessionHistory) {
	b.lifecycle = lifecycle
	b.skills = skills
	b.context = ctxProvider
	b.history = history
}

// History returns the session history accessor.
func (b *LaunchBackend) History() SessionHistory { return b.history }

// ManagedChatMCPServers returns the managed MCP servers composed for chat
// injection (ChatRequest.MCPServers), or nil when the lifecycle holds no
// managed payload or lacks the capability. A structured Execute path uses this
// to deliver the same server set Setup writes to the engine's settings file —
// probed by capability so a bare ManagedLifecycle fake stays valid.
func (b *LaunchBackend) ManagedChatMCPServers() []ChatMCPServer {
	if l, ok := b.lifecycle.(interface{ ChatMCPServers() []ChatMCPServer }); ok {
		return l.ChatMCPServers()
	}
	return nil
}

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
	b.TraceArgs(req.Verbosity, args, stderr)
	env := b.ExecuteEnv(req)
	if req.Mode == ModeInteractive {
		exitCode, err := b.RunInteractive(ctx, args, env, req.Stdin, stdout, stderr, req.Resize)
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

// ExecuteEnv assembles the child env: the request env plus the SCM
// context-file path when context was provided.
func (b *LaunchBackend) ExecuteEnv(req *ExecuteRequest) map[string]string {
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	if p := b.ContextFilePath(); p != "" {
		env[SCMContextFileEnv] = p
	}
	return env
}

// ContextFilePath returns the on-disk path of the provided context file, or ""
// when no context was provided. Execute passes it into the child env via the
// SCM context-file variable.
func (b *LaunchBackend) ContextFilePath() string {
	if b.context == nil {
		return ""
	}
	return b.context.GetContextFilePath()
}

// Setup prepares the backend for execution. The host resolves ctxloom
// config/bundles and ships the result in req.Managed, so Setup consumes only the
// wire-typed payload — it never imports config/bundles. It provides context,
// registers host-resolved slash commands, folds the host-assembled hooks + MCP
// into the lifecycle (appending the agent's own context-injection hook from the
// plugin-side context hash), and flushes hooks to the settings file. This flow
// is identical across launch agents, so it lives here.
func (b *LaunchBackend) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)

	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to provide context: %w", err)
	}

	// Native context delivery (claude): materialize the framed context file for
	// --append-system-prompt-file and suppress the SessionStart injection hook by
	// withholding the hash from MergeManaged. If materialization fails, keep the
	// hash so the hook still delivers context — never lose the user's context
	// (CLAUDE.md fault tolerance).
	contextHash := b.context.GetContextHash()
	if b.nativeContextDelivery && contextHash != "" {
		if path, err := WriteFramedContextFile(b.WorkDir(), contextHash); err != nil {
			fmt.Fprintf(os.Stderr, "ctxloom: warning: framed context materialize failed; keeping the injection hook: %v\n", err)
		} else if path != "" {
			b.nativeContextPath = path
			contextHash = ""
		}
	}

	if req.Managed != nil {
		if len(req.Managed.Skills) > 0 {
			if err := b.skills.RegisterFromContent(b.WorkDir(), req.Managed.Skills); err != nil {
				return fmt.Errorf("failed to register skills: %w", err)
			}
		}
		b.lifecycle.MergeManaged(req.Managed, b.WorkDir(), contextHash)
	}

	if err := b.lifecycle.Flush(b.WorkDir()); err != nil {
		return fmt.Errorf("failed to write hooks: %w", err)
	}

	return nil
}

// Cleanup releases resources after execution. Local-CLI agents hold none, so
// this is a no-op; an agent that needs teardown can override it.
func (b *LaunchBackend) Cleanup(ctx context.Context) error { return nil }
