package agent

import (
	"context"
	"fmt"
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
}

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

	if req.Managed != nil {
		if len(req.Managed.Skills) > 0 {
			if err := b.skills.RegisterFromContent(b.WorkDir(), req.Managed.Skills); err != nil {
				return fmt.Errorf("failed to register skills: %w", err)
			}
		}
		b.lifecycle.MergeManaged(req.Managed, b.WorkDir(), b.context.GetContextHash())
	}

	if err := b.lifecycle.Flush(b.WorkDir()); err != nil {
		return fmt.Errorf("failed to write hooks: %w", err)
	}

	return nil
}

// Cleanup releases resources after execution. Local-CLI agents hold none, so
// this is a no-op; an agent that needs teardown can override it.
func (b *LaunchBackend) Cleanup(ctx context.Context) error { return nil }
