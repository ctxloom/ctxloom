package agent

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// SessionHarpEnv is the env var carrying ctxloom's per-session harp name (e.g.
// "fair-pushy-cable"). The host sets it on the run env; Setup reads it to place
// session-scoped delivery scratch under the harp's private ephemeral dir.
const SessionHarpEnv = "CTXLOOM_SESSION_HARP"

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

	// delivery routes launch-time surface delivery through the runner-side
	// delivery seam (claude). When nil (antigravity/codex/kiro/acp) Setup takes
	// the legacy lifecycle path (Provide + MergeManaged + skills + Flush). When
	// set, Setup materializes each surface through the injected factory and
	// records the returned handles in delivered for teardown.
	delivery DeliveryFactory
	// delivered accumulates the handles Setup materialized through the delivery
	// seam, in delivery order. Cleanup reverses them LIFO.
	delivered []Delivered
}

// InitLaunch wires the constructed capabilities into the base. Call it from the
// concrete constructor once the capabilities (which usually close over the
// concrete backend) have been built. delivery is the runner-side delivery
// factory (claude); pass nil for a backend that keeps the legacy lifecycle path.
func (b *LaunchBackend) InitLaunch(lifecycle ManagedLifecycle, skills ContentSkills, ctxProvider HashedContext, history SessionHistory, delivery DeliveryFactory) {
	b.lifecycle = lifecycle
	b.skills = skills
	b.context = ctxProvider
	b.history = history
	b.delivery = delivery
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
// wire-typed payload — it never imports config/bundles. A backend without a
// delivery factory takes the legacy lifecycle path (Provide + MergeManaged +
// skills + Flush); a backend with one (claude) materializes each surface through
// the runner-side delivery seam and records the handles for teardown.
func (b *LaunchBackend) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if b.delivery != nil {
		return b.setupViaDelivery(req)
	}
	return b.setupViaLifecycle(req)
}

// setupViaLifecycle is the legacy path for backends without a delivery factory
// (antigravity/codex/kiro/acp): provide context, register slash commands, fold
// the host-assembled hooks + MCP into the lifecycle (appending the agent's own
// SessionStart context-injection hook from the plugin-side context hash), and
// flush hooks + MCP to the settings file. Behavior is unchanged from before the
// delivery seam.
func (b *LaunchBackend) setupViaLifecycle(req *SetupRequest) error {
	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		return fmt.Errorf("failed to provide context: %w", err)
	}
	contextHash := b.context.GetContextHash()

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

// setupViaDelivery materializes the loadout's surfaces through the injected
// delivery factory (claude). Context lands in the session's PRIVATE ephemeral
// dir (not the project tree) and rides claude's --append-system-prompt-file, so
// the SessionStart context-injection hook is suppressed (contextHash ""); the
// remaining surfaces (skills/settings/MCP) land in the working dir where the
// engine looks. Every delivered handle is recorded so Cleanup can reverse it.
//
// Fault tolerance (CLAUDE.md): if context delivery fails, fall back to the
// legacy SessionStart hook — materialize the raw cache file via Provide and keep
// the hash so MergeManaged re-appends the injection hook. Never lose the user's
// context to a scratch-write hiccup.
func (b *LaunchBackend) setupViaDelivery(req *SetupRequest) error {
	// Context surface → the session's private ephemeral dir (contextHash "" unless
	// delivery failed and fell back to the injection hook).
	contextHash := b.deliverContext(req)

	if req.Managed == nil {
		return nil
	}

	// Skills surface → the working directory (.claude/commands).
	if err := b.deliverSkills(req.Managed); err != nil {
		return err
	}

	// Fold the host-assembled hooks + MCP into the lifecycle. This performs the
	// same config/profile/bundle merge as the legacy path (so the merged set is
	// identical), with contextHash "" unless the context fallback kept it.
	b.lifecycle.MergeManaged(req.Managed, b.WorkDir(), contextHash)

	// Settings + MCP surfaces → the working directory.
	return b.deliverSettingsAndMCP(req.Managed)
}

// deliverContext materializes the context surface into the session's PRIVATE
// ephemeral dir and returns the hash to hand MergeManaged. The deduped assembly
// yields the exact string the framing wraps WITHOUT writing the raw cache file
// into the project tree. On success it records the handle and returns "" (the
// SessionStart context-injection hook is suppressed; context rides claude's
// --append-system-prompt-file). On a delivery error it falls back to the legacy
// hook — materialize the raw cache file via Provide and return its hash so
// MergeManaged re-appends the injection hook (CLAUDE.md fault tolerance: never
// lose the user's context to a scratch-write hiccup).
func (b *LaunchBackend) deliverContext(req *SetupRequest) string {
	strat := b.delivery.ContextDelivery(ephemeralPlacement{harp: req.Env[SessionHarpEnv]})
	if strat == nil {
		return ""
	}
	d, err := strat.DeliverContext(assembleDedupedContext(req.Fragments))
	if err == nil {
		b.delivered = append(b.delivered, d)
		return ""
	}
	fmt.Fprintf(os.Stderr, "ctxloom: warning: context delivery failed; keeping the injection hook: %v\n", err)
	if perr := b.context.Provide(b.WorkDir(), req.Fragments); perr == nil {
		return b.context.GetContextHash()
	}
	return ""
}

// deliverSkills materializes the skills surface (slash-command exports) into the
// working directory and records the handle.
func (b *LaunchBackend) deliverSkills(m *ManagedConfig) error {
	if len(m.Skills) == 0 {
		return nil
	}
	strat := b.delivery.SkillsDelivery(cwdPlacement{dir: b.WorkDir()})
	if strat == nil {
		return nil
	}
	d, err := strat.DeliverSkills(m.Skills)
	if err != nil {
		return fmt.Errorf("failed to deliver skills: %w", err)
	}
	b.delivered = append(b.delivered, d)
	return nil
}

// deliverSettingsAndMCP materializes the settings (hooks + statusline) and MCP
// surfaces into the working directory. When the lifecycle exposes its merged
// state, both ride the seam — writing byte-identical files to Flush and
// recording handles Cleanup can reverse — and Flush is SKIPPED (no
// double-write). Otherwise it falls back to Flush.
func (b *LaunchBackend) deliverSettingsAndMCP(m *ManagedConfig) error {
	workDir := b.WorkDir()
	hooks, mcp, ok := b.mergedState()
	settingsStrat := b.delivery.SettingsDelivery(cwdPlacement{dir: workDir})
	mcpStrat := b.delivery.MCPDelivery(cwdPlacement{dir: workDir})
	if !ok || settingsStrat == nil || mcpStrat == nil {
		if err := b.lifecycle.Flush(workDir); err != nil {
			return fmt.Errorf("failed to write hooks: %w", err)
		}
		return nil
	}

	d, err := settingsStrat.DeliverSettings(hooks, m.ManageStatusline)
	if err != nil {
		return fmt.Errorf("failed to deliver settings: %w", err)
	}
	b.delivered = append(b.delivered, d)

	dm, err := mcpStrat.DeliverMCP(mcp, m.BundleMCP)
	if err != nil {
		return fmt.Errorf("failed to deliver mcp: %w", err)
	}
	b.delivered = append(b.delivered, dm)
	return nil
}

// mergedState reads the lifecycle's merged hooks + MCP so the delivery seam can
// materialize the settings/MCP surfaces itself (in place of Flush). It probes by
// capability — mirroring ManagedChatMCPServers — so a bare ManagedLifecycle fake
// that lacks the accessors stays valid; ok is false then and Setup falls back to
// Flush. BaseLifecycle (every real launch backend's lifecycle) satisfies both.
func (b *LaunchBackend) mergedState() (hooks *wire.HooksConfig, mcp *wire.MCPConfig, ok bool) {
	lh, ok1 := b.lifecycle.(interface {
		GetHooks() *wire.HooksConfig
	})
	lm, ok2 := b.lifecycle.(interface {
		GetMCP() *wire.MCPConfig
	})
	if !ok1 || !ok2 {
		return nil, nil, false
	}
	return lh.GetHooks(), lm.GetMCP(), true
}

// Cleanup reverses the surfaces Setup delivered through the seam, in LIFO order
// (last delivered, first undone). It attempts every handle and returns the first
// error, so one surface's failed teardown never strands the rest. A backend that
// delivered nothing (the legacy lifecycle path, or a skip-setup run) holds no
// handles, so this is a no-op there.
func (b *LaunchBackend) Cleanup(ctx context.Context) error {
	var firstErr error
	for i := len(b.delivered) - 1; i >= 0; i-- {
		if err := b.delivered[i].Cleanup(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	b.delivered = nil
	return firstErr
}
