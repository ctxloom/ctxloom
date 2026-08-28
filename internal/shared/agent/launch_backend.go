package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// SessionHarpEnv is the env var carrying ctxloom's per-session harp name (e.g.
// "fair-pushy-cable"). The host sets it on the run env; Setup reads it to place
// session-scoped delivery scratch under the harp's private ephemeral dir.
const SessionHarpEnv = "CTXLOOM_SESSION_HARP"

// MCPCommandOverrideEnv carries an explicit override for the ctxloom MCP
// stdio command a run's MCP-surface writer materializes (.mcp.json,
// mcp_config.json, .kiro/settings/mcp.json, config.toml's [mcp_servers]),
// replacing CtxloomCommand()'s self-exec-absolute default (see
// ResolveMCPCommand). The host stamps it onto the run env ONLY for an
// isolated-container cell (isolation.Container.MCPCommandOverride via
// operations.MCPCommandOverrideForPolicy, cli/run.go) — the in-container
// ctxloom binary path (the surface used to always emit the HOST
// self-exec path, which does not exist inside the container, so the engine's
// `ctxloom mcp` stdio shim never launched and the child had zero MCP tools).
// Absent/empty everywhere else, which changes nothing.
const MCPCommandOverrideEnv = "CTXLOOM_MCP_COMMAND_OVERRIDE"

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

// LaunchBackend is the shared core of a local-CLI launch agent (claude).
// It owns the capability wiring (lifecycle/commands/context/history) and the
// generic Setup/Cleanup that every launch agent shares. A concrete agent embeds
// it, calls InitLaunch with its constructed capabilities, and implements only
// the genuinely engine-specific surface: Configure, Execute, and its config's
// BackendType.
type LaunchBackend struct {
	BaseBackend
	lifecycle ManagedLifecycle
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
// delivery (claude/codex/kiro); pass nil for a backend that keeps
// the legacy lifecycle path (acp).
func (b *LaunchBackend) InitLaunch(lifecycle ManagedLifecycle, ctxProvider HashedContext, history SessionHistory, delivery *CellDelivery) {
	b.lifecycle = lifecycle
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
// probed by capability so a bare ManagedLifecycle fake stays valid. override
// is ComposeChatMCPServers' command override — callers pass
// req.Env[MCPCommandOverrideEnv], populated ONLY for an isolated-container
// cell; empty everywhere else is a no-op.
func (b *LaunchBackend) ManagedChatMCPServers(override string) []ChatMCPServer {
	if l, ok := b.lifecycle.(interface{ ChatMCPServers(string) []ChatMCPServer }); ok {
		return l.ChatMCPServers(override)
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
	// Refuse an argv the OS cannot exec BEFORE trying, so the failure names
	// the payload rather than arriving as os/exec's generic "argument list too
	// long" — which points at the total argument list, the innocent part. This
	// lives here, once, because every exec-style backend funnels its launch
	// through this tail; the engines that carry the prompt on argv (codex,
	// kiro, and claude's interactive arm) are covered without each repeating
	// the check. See argvlimit.go.
	if err := checkArgvLimit(b.Name(), args, GetPromptContent(req.Prompt),
		singleArgLimit(runtime.GOOS, os.Getpagesize())); err != nil {
		return nil, err
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

// Setup prepares the backend for execution. The host resolves ctxloom
// config/bundles and ships the result in req.Managed, so Setup consumes only the
// wire-typed payload — it never imports config/bundles. Every launch backend
// supplies a CellDelivery at InitLaunch (a protocol-only backend like acp an empty
// one), so Setup builds the backend's SurfaceSet from the merged state and
// delivers it through the cell named by req.CellKind. A nil delivery is a
// misconfigured backend (InitLaunch was called without one) — never a
// legitimate "nothing to do" — so it fails loudly rather than reporting
// success while setting up nothing.
func (b *LaunchBackend) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if b.delivery == nil {
		return fmt.Errorf("%s: misconfigured backend: InitLaunch was called with a nil CellDelivery", b.Name())
	}
	return b.setupViaCells(req)
}

// setupViaCells is the generic surfaces × typed-cells Setup shared by every
// launch backend that routes delivery through the seam. It prepares the
// engine-consumed files in three steps:
//
//  1. RawContext pre-step — the file/hook engines (codex/kiro)
//     materialize the content-addressed cache file (agent.WriteContextFile via
//     Provide) and the CTXLOOM_CONTEXT_FILE env path. codex ALSO keys the
//     SessionStart context-injection hook to that hash (ContextHook) — and because
//     the cache file is on disk BEFORE MergeManaged runs, NewContextInjectionHooks
//     reads it and makes its chunk decision against the actual file.
//     kiro diverts context to AGENTS.md/steering (its context
//     surface), so its hook hash stays "". claude leaves RawContext false: its
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

	// 0. Enforce CellDelivery.ContextHook's stated precondition. The hook is keyed
	// to the RawContext cache file's hash, so without RawContext there is no hash
	// and no file: contextHash below stays "", MergeManaged appends no injection
	// hook, and the session launches with NO context while Setup reports success.
	// That is a misconfigured backend, not a legitimate "no context" case (which is
	// ContextHook false), so it fails loudly — same call as the mergedState
	// accessor check below.
	if d.ContextHook && !d.RawContext {
		return fmt.Errorf("backend delivery sets ContextHook without RawContext: the SessionStart injection hook is keyed to the RawContext cache file, so no hook can be installed")
	}

	// 1. RawContext pre-step (codex/kiro).
	contextHash := ""
	if d.RawContext {
		if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
			return fmt.Errorf("failed to provide context: %w", err)
		}
		if d.ContextHook {
			contextHash = b.context.GetContextHash()
		}
	}

	// A NIL payload means the config failed to load and the run degraded
	// through, so this returns without touching any surface — deliver nothing,
	// retract nothing. An EMPTY payload is a different fact and deliberately
	// does NOT stop here: it flows on to the writers, which reconcile to it and
	// retract what ctxloom installed last round. SetupRequest.Managed defines
	// both, and the difference is the whole reason this is not a len() check.
	//
	// The RawContext cache file, when written above, stands alone either way.
	if req.Managed == nil {
		return nil
	}

	// 2. Fold the host-assembled hooks + MCP into the lifecycle (the merge engine).
	// contextHash appends the injection hook only for the hook engine (codex).
	b.lifecycle.MergeManaged(req.Managed, b.WorkDir(), contextHash)

	// 3. Read the merged hooks + MCP so the settings/config surfaces write exactly
	// the merged state. `ok` used to be discarded, so a lifecycle lacking
	// the accessors (every production backend embeds BaseLifecycle, which has both —
	// this is defense against a future one that doesn't) fell through to building
	// the surface set with nil hooks/nil MCP as if that were the correctly-merged
	// state, silently writing a settings file containing none of the configured
	// hooks or servers. Fail loudly instead: this is a misconfigured backend, not a
	// legitimate "nothing configured" case (that is an EMPTY payload, which flows
	// straight past here and reconciles; see SetupRequest.Managed).
	hooks, bundleMCP, ok := b.mergedState()
	if !ok {
		return fmt.Errorf("backend lifecycle does not expose the merged hooks/MCP state (GetHooks/GetBundleMCP) needed to deliver surfaces")
	}

	assembled, err := assembleSurfaceContext(req.Fragments)
	if err != nil {
		return err
	}

	inputs := SurfaceInputs{
		Context:            assembled,
		Fragments:          req.Fragments,
		BundleMCP:          bundleMCP,
		Hooks:              hooks,
		ManageStatusline:   req.Managed.ManageStatusline,
		Commands:           req.Managed.Commands,
		Skills:             req.Managed.Skills,
		MCPCommandOverride: req.Env[MCPCommandOverrideEnv],
		DenyTools:          req.Managed.DenyTools,
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

// assembleSurfaceContext assembles the context a surface-delivering backend
// hands to its context surface, and refuses an empty result the caller did not
// ask for. No fragments legitimately assembles to nothing — that project
// configured no context — but fragments that all resolve to zero bytes is a
// different fact: the user asked for context and would get none, with no file
// written, no launch flag emitted and nothing said about it.
//
// The raw-cache path already refuses exactly this input (WriteContextFile
// returns ErrNoContext, and Provide propagates it), so the same error here is
// what stops the surface path from being the one delivery that launches a
// context-less session and reports success. Callers that genuinely tolerate an
// empty assembly can test errors.Is(err, ErrNoContext) — the point is that they
// have to say so.
func assembleSurfaceContext(fragments []*Fragment) (string, error) {
	assembled := assembleDedupedContext(fragments)
	if assembled == "" && len(fragments) > 0 {
		return "", fmt.Errorf("%w: %d fragment(s) produced zero bytes", ErrNoContext, len(fragments))
	}
	return assembled, nil
}

// preferSharedRealization retargets kind's currently-selected approach (as
// WithEverything left it — the backend's table default) to the first
// backend-supported approach that HAS a shared realization, when the
// currently-selected approach does not have one. It is deliverSet's
// SharedCell default-derivation step (U100-F05) and nothing else calls it: an
// at-rest selection (DeliverUnder / profile materialize) never reaches this
// method, so its table default (claude context's unsafe-file) is untouched
// there, and profile_materialize.go's explicit
// WithApproach(SurfaceContext, ApproachUnsafeFile) still names its own
// default independently. For claude this turns the table default (unsafe-file
// — approach.go:44-50 explains why no table order can make system-prompt the
// default there) into system-prompt for a no-preference SHARED launch,
// preserving the scratch-write behaviour every claude launch had before this
// pair-key existed. mcp/settings already realize at their sole approach, so
// this is a no-op for them; a backend with no realization at all (codex/kiro/
// opencode/mock) leaves every kind exactly as WithEverything set it.
func (s *SurfaceSelection) preferSharedRealization(kind SurfaceKind) {
	cur, ok := s.approaches[kind]
	if !ok {
		return
	}
	if _, ok := s.set.SharedRealization(kind, cur); ok {
		return
	}
	for _, a := range s.set.SupportedApproaches(kind) {
		if _, ok := s.set.SharedRealization(kind, a); ok {
			s.approaches[kind] = a
			return
		}
	}
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
// req is NON-NIL: the sole caller has already dereferenced it (req.Fragments,
// req.Managed.BundleMCP, req.CellKind) to build the set it passes here, so a nil
// req would have panicked well before this call. Two `req != nil` guards used to
// sit either side of the CellKindShared block below, which dereferences req
// unconditionally — staticcheck read that ordering as "the author thinks this can
// be nil, and then derefs it anyway" (SA5011). Guarding the middle block would
// have encoded a contract the caller cannot honor; stating the contract is the
// truthful fix.
func (b *LaunchBackend) deliverSet(set SurfaceSet, req *SetupRequest) error {
	// Launch delivers the WHOLE surface set, so it drives the builder over the same
	// full selection materialize/apply use — the builder is the single selection
	// input everywhere. Launch KEEPS its own cell machinery (a SharedCell over the
	// SharedRealization-converted / warned-native set, an isolated cell over the
	// well-known set): only the surface LIST is derived from the selection, not the
	// cell/placement choice.
	sel := Select(set).WithEverything()
	explicit := map[SurfaceKind]bool{}
	if req.Managed != nil {
		for kind := range req.Managed.Surfaces {
			explicit[kind] = true
		}
	}
	// U100-F05's default-derivation step: a SHARED-cwd launch with no explicit
	// per-surface preference should still prefer a race-safe realization over
	// the table's at-rest default (unsafe-file) — that default exists for
	// DeliverUnder, which has no argv sink for anything else, not for a launch,
	// which does. Restricted to CellKindShared (an isolated cell's well-known
	// write is already race-free by construction — see Deliveries' doc — so
	// there is nothing to prefer) and skipped for any kind the caller named
	// explicitly (req.Managed.Surfaces, applied below): an explicit
	// context=unsafe-file preference must be HONORED, not silently converted
	// back to the scratch (the fork DECIDED for U100-F05).
	if req.CellKind == CellKindShared {
		for kind := range sel.approaches {
			if !explicit[kind] {
				sel.preferSharedRealization(kind)
			}
		}
	}
	// The agent binding's preference, applied where it is actually valid: a
	// launch has the argv sink system-prompt needs, which is exactly why this
	// belongs on the agent rather than in the engine's table (an at-rest
	// DeliverUnder inheriting it would fail for want of one).
	if req.Managed != nil {
		for kind, approach := range req.Managed.Surfaces {
			sel = sel.WithApproach(kind, approach)
		}
	}
	resolved, err := sel.Build()
	if err != nil {
		return err
	}

	if req.CellKind == CellKindShared {
		for _, rs := range resolved.surfaces {
			d, err := resolved.deliverOneShared(rs, b.WorkDir())
			if err != nil {
				// This used to be matched by INDEX 0 rather than by kind, coupling
				// this loop to cells.go's surfaceOrder by position — a backend with
				// no distinct context surface (its context rides another surface)
				// has resolved.surfaces[0] be something else (MCP), and that
				// surface's failure was misidentified as the context failure this
				// fallback exists for.
				if rs.kind == SurfaceContext && !b.delivery.RawContext && b.recoverContextViaHook(req, err) {
					continue
				}
				return fmt.Errorf("failed to deliver surface into shared cwd: %w", err)
			}
			// A caller-selected ApproachHook context surface delivers successfully
			// as a documented no-op WRITE (noopContextDelivery: err==nil, d==nil) —
			// the design intent is that the settings-carried SessionStart hook
			// itself carries the context, but nothing installs that hook unless
			// something does so here. Without this, the err==nil branch above never
			// runs recoverContextViaHook (it fires only on a write FAILURE), so a
			// deliberately-pinned hook approach silently launched a context-less
			// session while Setup reported success — CONTEXT-DELIVERY was
			// measured empty though assembly and hook-firing both succeeded.
			// Scoped to SharedCell/SurfaceContext/ApproachHook exactly like the
			// failure fallback above; !b.delivery.RawContext guards a future
			// RawContext-and-Hook backend, whose setupViaCells pre-step already
			// installed the hook, from a double append.
			if rs.kind == SurfaceContext && rs.approach == ApproachHook && !b.delivery.RawContext {
				if !b.installContextInjectionHook(req) {
					return fmt.Errorf("failed to install the context-injection hook for surface %s", rs.kind)
				}
			}
			if d != nil { // a no-op delivery (wrote nothing) holds no cleanup handle
				b.delivered = append(b.delivered, d)
			}
		}
		return nil
	}

	// DirectoryIsolatedCell/ProcessIsolatedCell used to be two distinct
	// types chosen between here on req.CellKind, but both were
	// `isolatedCell{dir}` with no added field or method — the branch had no
	// observable effect. Collapsed to one IsolatedCell; the CellKind
	// distinction survives where it actually matters (buildArgs/env).
	cell := NewIsolatedCell(b.WorkDir())
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

// installContextInjectionHook materializes the raw context cache file (via
// Provide) and appends the SessionStart injection hook keyed to its hash
// directly onto the shared merged hooks — the very *wire.HooksConfig the
// settings surface (delivered next in the same SharedCell loop) will write.
// It is the one mechanism that actually gets hook-carried context to a
// flag-context backend (claude): both recoverContextViaHook's failure
// fallback and a deliberately-selected ApproachHook context surface (a
// documented no-op WRITE — see noopContextDelivery — that otherwise installs
// nothing at all) route through here. It appends ONLY the injection hook
// (never re-runs MergeManaged, which would clobber the statusline state).
// Reports whether the install took hold.
func (b *LaunchBackend) installContextInjectionHook(req *SetupRequest) bool {
	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		Warn("context-injection hook install: Provide failed: %v", err)
		return false
	}
	hash := b.context.GetContextHash()
	if hash == "" {
		return true // empty context: nothing to inject, and nothing was lost.
	}
	hooks, _, ok := b.mergedState()
	if !ok || hooks == nil {
		Warn("context-injection hook install: could not read the merged hooks state")
		return false
	}
	hooks.Unified.SessionStart = append(hooks.Unified.SessionStart,
		NewContextInjectionHooks(hash, b.WorkDir())...)
	return true
}

// recoverContextViaHook is the SharedCell context-delivery fallback for a
// flag-context backend (claude): when the out-of-cwd context surface fails to
// write its scratch file, fall back to installContextInjectionHook instead of
// losing the user's context to a scratch-write hiccup. Reports whether the
// fallback took hold (the caller then skips the failed context handle and
// continues delivering the remaining surfaces). cause is the error that
// triggered the fallback — it used to be discarded, and the warning went
// straight to os.Stderr rather than this package's own Warn/clidiag sink —
// the mechanism a session that owns the terminal uses to keep a warning from
// corrupting a live TUI frame it is painting.
func (b *LaunchBackend) recoverContextViaHook(req *SetupRequest, cause error) bool {
	Warn("context delivery failed (%v); keeping the injection hook", cause)
	return b.installContextInjectionHook(req)
}

// mergedState reads the lifecycle's merged hooks + MCP so the delivery seam can
// materialize the settings/MCP surfaces itself. It probes by
// capability — mirroring ManagedChatMCPServers — so a bare ManagedLifecycle fake
// that lacks the accessors stays valid; ok is false then. BaseLifecycle (every
// real launch backend's lifecycle) satisfies both, so ok is true in practice.
func (b *LaunchBackend) mergedState() (hooks *wire.HooksConfig, bundleMCP map[string]wire.MCPServer, ok bool) {
	lh, ok1 := b.lifecycle.(interface {
		GetHooks() *wire.HooksConfig
	})
	lm, ok2 := b.lifecycle.(interface {
		GetBundleMCP() map[string]wire.MCPServer
	})
	if !ok1 || !ok2 {
		return nil, nil, false
	}
	return lh.GetHooks(), lm.GetBundleMCP(), true
}

// Cleanup reverses the surfaces Setup delivered through the seam, in LIFO order
// (last delivered, first undone). It attempts every handle regardless of
// earlier failures, so one surface's failed teardown never strands the rest,
// and joins every failure into the returned error (it used to keep only the
// first, silently discarding the rest even though every handle was still
// attempted) — errors.Is/As still find any individual cause. A backend
// that delivered nothing (the legacy lifecycle path, or a skip-setup run)
// holds no handles, so this is a no-op there.
func (b *LaunchBackend) Cleanup(ctx context.Context) error {
	var errs []error
	for i := len(b.delivered) - 1; i >= 0; i-- {
		if err := b.delivered[i].Cleanup(); err != nil {
			errs = append(errs, err)
		}
	}
	b.delivered = nil
	return errors.Join(errs...)
}
