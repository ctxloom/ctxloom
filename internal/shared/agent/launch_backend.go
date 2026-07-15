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

// ManagedLifecycle folds a host-assembled ManagedConfig into its managed hooks +
// MCP; the surfaces × cells Setup then reads the merged state (GetHooks/GetMCP)
// to write each settings/config surface. BaseLifecycle implements it.
type ManagedLifecycle interface {
	MergeManaged(m *ManagedConfig, workDir, contextHash string)
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

// ContentCommands registers host-resolved command exports (slash commands)
// directly. The host maps bundle content to engine-agnostic CommandExports and
// the agent writes them in its native command format.
type ContentCommands interface {
	RegisterFromContent(workDir string, cmds []CommandExport) error
}

// LaunchBackend is the shared core of a local-CLI launch agent (claude/antigravity).
// It owns the capability wiring (lifecycle/commands/context/history) and the
// generic Setup/Cleanup that every launch agent shares. A concrete agent embeds
// it, calls InitLaunch with its constructed capabilities, and implements only
// the genuinely engine-specific surface: Configure, Execute, and its config's
// BackendType.
type LaunchBackend struct {
	BaseBackend
	lifecycle ManagedLifecycle
	commands  ContentCommands
	context   HashedContext
	history   SessionHistory

	// delivery routes launch-time surface delivery through the surfaces × typed-
	// cells seam. Every launch backend supplies one at InitLaunch (acp an empty-set
	// one — it materializes no files); Setup builds the backend's SurfaceSet from
	// the merged state and delivers it through the cell named by req.CellKind,
	// recording the returned handles in delivered for teardown. A nil delivery is a
	// misconfigured backend, so Setup no-ops rather than panics.
	delivery *CellDelivery
	// extraEnv, when set, contributes per-backend child-env entries on top of the
	// shared ExecuteEnv (the request env + the SCM context-file path) — the seam a
	// cell-aware backend (codex's cell-scoped CODEX_HOME) uses to compute env from
	// the request without reimplementing the shared assembly.
	extraEnv func(req *ExecuteRequest) map[string]string
	// delivered accumulates the handles Setup materialized through the delivery
	// seam, in delivery order. Cleanup reverses them LIFO.
	delivered []Delivered
}

// InitLaunch wires the constructed capabilities into the base. Call it from the
// concrete constructor once the capabilities (which usually close over the
// concrete backend) have been built. delivery configures cell-based surface
// delivery (claude/codex/antigravity/kiro); pass nil for a backend that keeps
// the legacy lifecycle path (acp).
func (b *LaunchBackend) InitLaunch(lifecycle ManagedLifecycle, commands ContentCommands, ctxProvider HashedContext, history SessionHistory, delivery *CellDelivery) {
	b.lifecycle = lifecycle
	b.commands = commands
	b.context = ctxProvider
	b.history = history
	b.delivery = delivery
}

// SetExecuteEnv registers a per-backend child-env contributor merged into
// ExecuteEnv. A cell-aware backend (codex) uses it to inject cell-scoped env
// (CODEX_HOME) computed from the ExecuteRequest, without reimplementing the
// shared env assembly. Later entries win over the shared ones on a key clash.
func (b *LaunchBackend) SetExecuteEnv(fn func(req *ExecuteRequest) map[string]string) {
	b.extraEnv = fn
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

// ExecuteEnv assembles the child env: the request env, the SCM context-file path
// when context was provided, and any per-backend contributor (SetExecuteEnv).
func (b *LaunchBackend) ExecuteEnv(req *ExecuteRequest) map[string]string {
	env := make(map[string]string, len(req.Env)+1)
	for k, v := range req.Env {
		env[k] = v
	}
	if p := b.ContextFilePath(); p != "" {
		env[SCMContextFileEnv] = p
	}
	if b.extraEnv != nil {
		for k, v := range b.extraEnv(req) {
			env[k] = v
		}
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
// wire-typed payload — it never imports config/bundles. Every launch backend
// supplies a CellDelivery at InitLaunch (a protocol-only backend like acp an empty
// one), so Setup builds the backend's SurfaceSet from the merged state and
// delivers it through the cell named by req.CellKind. A nil delivery is a
// misconfigured backend — nothing to set up — so it no-ops rather than panics.
func (b *LaunchBackend) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if b.delivery == nil {
		return nil
	}
	return b.setupViaCells(req)
}

// setupViaCells is the generic surfaces × typed-cells Setup shared by every
// launch backend that routes delivery through the seam. It prepares the
// engine-consumed files in three steps:
//
//  1. RawContext pre-step — the file/hook engines (codex/antigravity/kiro)
//     materialize the content-addressed cache file (agent.WriteContextFile via
//     Provide) and the CTXLOOM_CONTEXT_FILE env path. codex ALSO keys the
//     SessionStart context-injection hook to that hash (ContextHook) — and because
//     the cache file is on disk BEFORE MergeManaged runs, NewContextInjectionHooks
//     reads it and makes its chunk decision against the actual file.
//     antigravity/kiro divert context to AGENTS.md/steering (their context
//     surface), so their hook hash stays "". claude leaves RawContext false: its
//     context rides an out-of-cwd flag or a well-known CLAUDE.md, never the cache
//     file.
//  2. MergeManaged folds the host-assembled config/bundle payload into the
//     lifecycle (the merge engine) keyed by contextHash.
//  3. The backend's Build closure turns the merged state into its SurfaceSet, and
//     the cell named by req.CellKind delivers each surface — a SharedCell over the
//     race-safe set (out-of-cwd flag files / warned Unsafe), an isolated cell over
//     the well-known set (native files in the private dir).
//
// The req.Managed == nil short-circuit and the LIFO Cleanup are preserved.
func (b *LaunchBackend) setupViaCells(req *SetupRequest) error {
	d := b.delivery

	// 1. RawContext pre-step (codex/antigravity/kiro).
	contextHash := ""
	if d.RawContext {
		if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
			return fmt.Errorf("failed to provide context: %w", err)
		}
		if d.ContextHook {
			contextHash = b.context.GetContextHash()
		}
	}

	// Nothing managed → no surfaces to deliver: with no hooks/MCP/commands to write,
	// there are no settings/config surfaces. The RawContext cache file, when written
	// above, stands alone.
	if req.Managed == nil {
		return nil
	}

	// 2. Fold the host-assembled hooks + MCP into the lifecycle (the merge engine).
	// contextHash appends the injection hook only for the hook engine (codex).
	b.lifecycle.MergeManaged(req.Managed, b.WorkDir(), contextHash)

	// 3. Read the merged hooks + MCP so the settings/config surfaces write exactly
	// the merged state.
	hooks, mcp, _ := b.mergedState()

	inputs := SurfaceInputs{
		Context:          assembleDedupedContext(req.Fragments),
		Fragments:        req.Fragments,
		MCP:              mcp,
		BundleMCP:        req.Managed.BundleMCP,
		Hooks:            hooks,
		ManageStatusline: req.Managed.ManageStatusline,
		Commands:         req.Managed.Commands,
	}

	// A SharedCell's race-safe surfaces land in the session's PRIVATE ephemeral dir
	// (out-of-cwd flag files); an isolated cell's well-known surfaces land in the
	// private working dir itself.
	isolatedDir := b.WorkDir()
	if req.CellKind == CellKindShared {
		isolatedDir = ephemeralPlacement{harp: req.Env[SessionHarpEnv]}.Dir()
	}
	set := d.Build(inputs, isolatedDir)

	return b.deliverSet(set, req)
}

// deliverSet delivers every surface of set through the cell named by
// req.CellKind, recording each returned handle so Cleanup can reverse it (LIFO).
// A SharedCell takes the race-safe set (out-of-cwd flag files, or a warned Unsafe
// well-known write) prepared for the live working dir; an isolated cell takes the
// plain well-known set written into its private directory (the working dir, which
// the worktree/container private-checkout makes non-racy).
//
// Fault tolerance (CLAUDE.md, item 6): for a flag-context backend (claude) the
// context surface is FIRST, and its out-of-cwd scratch write is the one delivery
// whose loss would strand the user's context. If it fails, fall back to the legacy
// SessionStart injection hook — Provide the raw cache file and append the injection
// hook to the shared merged hooks, which the not-yet-delivered settings surface
// then writes — rather than launching a context-less session.
func (b *LaunchBackend) deliverSet(set SurfaceSet, req *SetupRequest) error {
	// Launch delivers the WHOLE surface set, so it drives the builder over the same
	// full selection materialize/apply use — the builder is the single selection
	// input everywhere. Launch KEEPS its own cell machinery (a SharedCell over the
	// SharedRealization-converted / warned-native set, an isolated cell over the
	// well-known set): only the surface LIST is derived from the selection, not the
	// cell/placement choice.
	resolved, err := Select(set).WithEverything().Build()
	if err != nil {
		return err
	}

	if req.CellKind == CellKindShared {
		for i, rs := range resolved.surfaces {
			d, err := resolved.deliverOneShared(rs, b.WorkDir())
			if err != nil {
				if i == 0 && !b.delivery.RawContext && b.recoverContextViaHook(req) {
					continue
				}
				return fmt.Errorf("failed to deliver surface into shared cwd: %w", err)
			}
			if d != nil { // a no-op delivery (wrote nothing) holds no cleanup handle
				b.delivered = append(b.delivered, d)
			}
		}
		return nil
	}

	var cell interface {
		Deliver(Delivery) (Delivered, error)
	}
	if req.CellKind == CellKindProcessIsolated {
		cell = NewProcessIsolatedCell(b.WorkDir())
	} else {
		cell = NewDirectoryIsolatedCell(b.WorkDir())
	}
	for _, kd := range resolved.Deliveries() {
		d, err := cell.Deliver(kd)
		if err != nil {
			return fmt.Errorf("failed to deliver surface: %w", err)
		}
		if d != nil { // a no-op delivery (wrote nothing) holds no cleanup handle
			b.delivered = append(b.delivered, d)
		}
	}
	return nil
}

// recoverContextViaHook is the SharedCell context-delivery fallback for a
// flag-context backend (claude): when the out-of-cwd context surface fails to
// write its scratch file, materialize the raw cache file via Provide and append
// the SessionStart injection hook keyed to its hash directly onto the shared
// merged hooks — the very *wire.HooksConfig the settings surface (delivered next
// in the same loop) will write. The session then launches with context via the
// legacy hook rather than losing it to a scratch-write hiccup. It appends ONLY the
// injection hook (never re-runs MergeManaged, which would clobber the statusline
// state). Reports whether the fallback took hold (the caller then skips the failed
// context handle and continues delivering the remaining surfaces).
func (b *LaunchBackend) recoverContextViaHook(req *SetupRequest) bool {
	fmt.Fprintf(os.Stderr, "ctxloom: warning: context delivery failed; keeping the injection hook\n")
	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		return false
	}
	hash := b.context.GetContextHash()
	if hash == "" {
		return true // empty context: nothing to inject, and nothing was lost.
	}
	hooks, _, ok := b.mergedState()
	if !ok || hooks == nil {
		return false
	}
	hooks.Unified.SessionStart = append(hooks.Unified.SessionStart,
		NewContextInjectionHooks(hash, b.WorkDir())...)
	return true
}

// mergedState reads the lifecycle's merged hooks + MCP so the delivery seam can
// materialize the settings/MCP surfaces itself. It probes by
// capability — mirroring ManagedChatMCPServers — so a bare ManagedLifecycle fake
// that lacks the accessors stays valid; ok is false then. BaseLifecycle (every
// real launch backend's lifecycle) satisfies both, so ok is true in practice.
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
