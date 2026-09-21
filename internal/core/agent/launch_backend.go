package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/core/wire"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// SessionHarpEnv is sessions.EnvHarp under this package's established name:
// the env var carrying ctxloom's per-session harp name. The host sets it on
// the run env; Setup reads it to place session-scoped delivery scratch under
// the harp's private ephemeral dir.
const SessionHarpEnv = sessions.EnvHarp

// ManagedLifecycle folds a host-assembled ManagedConfig into its managed hooks +
// MCP; the surfaces × cells Setup then reads the merged state (GetHooks/GetMCP)
// to write each settings/config surface. BaseLifecycle implements it.
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
// generic Setup/Cleanup that every launch agent shares. A concrete agent embeds
// it, calls InitLaunch with its constructed capabilities, and implements only
// the genuinely engine-specific surface: Configure, Execute, and its config's
// BackendType.
type LaunchBackend struct {
	BaseBackend
	lifecycle ManagedLifecycle
	context   HashedContext
	history   SessionHistory

	// surfaces is the engine's static Declaration: which approaches it
	// constructs for each surface kind. Setup selects from it, constructs the
	// selected approaches from the merged state, and delivers them through the
	// cell named by req.CellKind, recording the returned handles in delivered
	// for teardown. A protocol-only engine that materializes no files declares
	// nothing (an empty Declaration); a nil one is a misconfigured backend and
	// Setup fails loudly rather than reporting success while setting up nothing.
	surfaces Declaration
	// resolved is the selection Setup built and delivered for the current run,
	// exposed via Resolved so an engine can read what its own approaches
	// recorded (claude's out-of-cwd file paths) when it builds argv.
	resolved *ResolvedSelection
	// extraEnv, when set, contributes per-backend child-env entries on top of the
	// shared ExecuteEnv (the request env + the SCM context-file path) — the seam a
	// cell-aware backend (codex's cell-scoped CODEX_HOME) uses to compute env from
	// the request without reimplementing the shared assembly.
	extraEnv func(req *ExecuteRequest) map[string]string
	// engineHomeVar names the env var that relocates this engine's config
	// home (claude's CLAUDE_CONFIG_DIR). setupViaCells reads THIS RUN's value
	// of it as the EngineHome root every writer is advised. Empty — an engine
	// that never declared one — advises no EngineHome, whatever the run env
	// happens to carry: the root is a fact the engine states, never inferred
	// from the environment.
	engineHomeVar string
	// delivered accumulates the handles Setup materialized through the delivery
	// seam, in delivery order. Cleanup reverses them LIFO.
	delivered []Delivered
	// minimal is the engine's declared MINIMAL launch posture (MinimalLaunch),
	// registered at construction. nil for an engine that declares none.
	minimal MinimalLaunch
	// minimalArgs is that posture RESOLVED for this run — non-nil only after a
	// Setup on LaunchFormMinimal. It is what makes the minimal run's argv a
	// thing Setup decided rather than a branch every argv site takes on a
	// request flag.
	minimalArgs []string
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

// Resolved returns the selection Setup built and delivered for the current
// run, or nil before Setup ran (or when it delivered nothing). An engine reads
// it to learn what its own approaches recorded — never to deliver again.
func (b *LaunchBackend) Resolved() *ResolvedSelection { return b.resolved }

// SetMinimalLaunch registers the engine's declared minimal launch posture —
// the argv that strips it back to a bare model call for a headless run. Call it
// from the concrete constructor, beside InitLaunch. An engine that declares
// none launches bare on LaunchFormMinimal.
func (b *LaunchBackend) SetMinimalLaunch(m MinimalLaunch) { b.minimal = m }

// MinimalArgs returns the minimal launch posture Setup RESOLVED for this run,
// or nil on every other form (and before Setup). An engine appends it to its
// argv unconditionally: it is empty unless this run declared LaunchFormMinimal,
// so there is nothing left for buildArgs to decide.
func (b *LaunchBackend) MinimalArgs() []string { return b.minimalArgs }

// SetExecuteEnv registers a per-backend child-env contributor merged into
// ExecuteEnv. A cell-aware backend (codex) uses it to inject cell-scoped env
// (CODEX_HOME) computed from the ExecuteRequest, without reimplementing the
// shared env assembly. Later entries win over the shared ones on a key clash.
func (b *LaunchBackend) SetExecuteEnv(fn func(req *ExecuteRequest) map[string]string) {
	b.extraEnv = fn
}

// SetEngineHomeVar names the env var that relocates this engine's config
// home, so a run that carries it (an agent binding with engine_home: session)
// advises its private engine home to every writer. See engineHomeVar.
func (b *LaunchBackend) SetEngineHomeVar(name string) { b.engineHomeVar = name }

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

// Setup prepares the backend for execution. The host resolves ctxloom
// config/bundles and ships the result in req.Managed, so Setup consumes only the
// wire-typed payload — it never imports config/bundles. Every launch backend
// supplies its Declaration at InitLaunch (a protocol-only backend an empty
// one), so Setup selects from it, constructs from the merged state, and
// delivers through the cell named by req.CellKind. A nil Declaration is a
// misconfigured backend (InitLaunch was never called) — never a legitimate
// "nothing to do" — so it fails loudly rather than reporting success while
// setting up nothing.
func (b *LaunchBackend) Setup(ctx context.Context, req *SetupRequest) error {
	b.SetWorkDir(req.WorkDir)
	if b.surfaces == nil {
		return fmt.Errorf("%s: misconfigured backend: InitLaunch was never called, so this backend declares no surfaces", b.Name())
	}
	return b.setupViaCells(req)
}

// setupViaCells is the generic surfaces × typed-cells Setup shared by every
// launch backend that routes delivery through the seam. It prepares the
// engine-consumed files in two steps:
//
//  1. MergeManaged folds the host-assembled config/bundle payload into the
//     lifecycle (the merge engine).
//  2. The selection is built from the engine's Declaration and the merged
//     state, the run's roots are resolved and advised ONCE, and the cell named
//     by req.CellKind delivers each surface against them — a SharedCell over
//     the race-safe set (out-of-cwd flag files / warned Unsafe), an isolated
//     cell over the well-known set (native files in the private dir).
//
// Context that rides a hook (an approach declaring Rider for the context
// surface) is installed by deliverSet once the selection is known, on every
// cell, so there is no pre-step here and nothing for one to get wrong.
func (b *LaunchBackend) setupViaCells(req *SetupRequest) error {
	// LaunchFormMinimal declares NO managed surfaces: a headless run
	// (distillation, compaction, triage) is a bare model call. Setup still
	// runs — this is a resolved form, not a bypass — and what it resolves is
	// the engine's declared minimal posture, which Execute then emits like any
	// other resolved argv. Nothing is delivered, so nothing is recorded for
	// Cleanup, and req.Managed is not consulted at all: a minimal run carries
	// none, and that absence is a DECLARATION here rather than the failed-config
	// nil the check below answers.
	if req.Form == LaunchFormMinimal {
		if b.minimal != nil {
			b.minimalArgs = b.minimal.MinimalArgs(req.Model)
		}
		return nil
	}

	// A NIL payload means the config failed to load and the run degraded
	// through, so this returns without touching any surface — deliver nothing,
	// retract nothing. An EMPTY payload is a different fact and deliberately
	// does NOT stop here: it flows on to the writers, which reconcile to it and
	// retract what ctxloom installed last round. SetupRequest.Managed defines
	// both, and the difference is the whole reason this is not a len() check.
	if req.Managed == nil {
		return nil
	}

	// 1. Fold the host-assembled hooks + MCP into the lifecycle (the merge
	// engine). The context hash is "" here: hook-carried context is installed
	// AFTER the selection resolves (deliverSet), against these same merged
	// hooks, so it lands only when the selected context approach actually
	// rides the hook.
	b.lifecycle.MergeManaged(report.To(req.Reporter), req.Managed, b.WorkDir(), "")

	// 2. Read the merged hooks + MCP so the settings/config surfaces write exactly
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
		Reporter:         req.Reporter,
		Context:          assembled,
		Fragments:        req.Fragments,
		BundleMCP:        bundleMCP,
		Hooks:            hooks,
		ManageStatusline: req.Managed.ManageStatusline,
		Commands:         req.Managed.Commands,
		Skills:           req.Managed.Skills,
		DenyTools:        req.Managed.DenyTools,
	}

	// The run's roots, resolved and advised ONCE, before any surface runs. The
	// project root is the working dir; Scratch is where a SharedCell's race-safe
	// surfaces land (the session's PRIVATE ephemeral dir, out of the shared
	// cwd), while an isolated cell's private working dir is its own scratch.
	//
	// present.OnHost, not Containerize: Setup runs where the engine runs — for a
	// container cell, inside it — so the writer and the engine share one
	// filesystem namespace and the identity advice is the truthful one here.
	// A Containerize advice belongs to whoever launches the container, before
	// Setup ever runs in it.
	scratch := b.WorkDir()
	if req.CellKind == CellKindShared {
		s, err := sharedScratchDir(req.Env[SessionHarpEnv])
		if err != nil {
			return err
		}
		scratch = s
	}
	//
	// EngineHome is the engine's PRIVATE config home for this run, read from
	// the var the engine declared (SetEngineHomeVar). A run with none — a
	// binding that selected the real host home, an engine with no
	// relocatable home — advises no EngineHome,
	// and an approach that writes beneath it refuses (ErrUnrootedEngineHome)
	// rather than landing in the user's own home.
	var engineHome string
	if b.engineHomeVar != "" {
		engineHome = req.Env[b.engineHomeVar]
	}
	start := present.New(present.OnHost(present.Paths{
		ProjectRoot: present.Root{Host: b.WorkDir()},
		EngineHome:  present.Root{Host: engineHome},
		Scratch:     present.Root{Host: scratch},
	}))

	return b.deliverSet(inputs, req, start)
}

// ErrSharedScratchNoHarp is returned by sharedScratchDir when it has no harp
// (or an unresolvable one) to derive a private scratch dir from.
//
// RULED 2026-09-12: an empty or unresolvable harp reaching sharedScratchDir is
// a PROGRAMMING ERROR, not a case to absorb. This function used to fall back
// to os.TempDir() — a silent substitution of a world-readable shared location
// for the session's private ephemeral dir, which is exactly the shape the
// project's no-degradation delivery rule forbids (the private engine home is
// the root; a shared/world-readable location is used only when explicitly
// selected, never reached by accident). Keeping the fallback "documented" was
// considered and struck: it would carve a permanent exception into a rule the
// project states absolutely, and exceptions to no-degradation rules get cited
// as precedent. See taskloom row urgent-staunch.
//
// The audit behind this ruling (same row) found exactly one production path
// to this function (setupViaCells, gated on req.CellKind == CellKindShared).
// Every launch that reaches it carries a harp by construction: every launch
// is minted before it is resolved (operations.StartRun), and the codec
// stamps the identity carriers onto the request (coordgrpc.EncodeLaunch)
// — the auth probe and the internal one-shots included, each a session of
// its own. Do not paper over a missing harp here with a synthesised one,
// which is what this whole doc exists to forbid.
var ErrSharedScratchNoHarp = errors.New("delivery: sharedScratchDir has no harp to derive a private scratch dir from — this is a programming error in the caller, not a case to fall back from; CellKindShared delivery must carry a resolvable CTXLOOM_SESSION_HARP")

// sharedScratchDir is where a SharedCell's race-safe surfaces land: the
// session's regenerable ephemeral directory (paths.HarpEphemeralDir), which is
// PRIVATE to the run and out of the shared cwd. An empty or unresolvable harp
// refuses loudly (ErrSharedScratchNoHarp) rather than silently substituting
// the OS temp dir — see ErrSharedScratchNoHarp's doc for the ruling and the
// caller audit behind it.
func sharedScratchDir(harp string) (string, error) {
	if harp == "" {
		return "", ErrSharedScratchNoHarp
	}
	dir, err := paths.HarpEphemeralDir(harp)
	if err != nil {
		return "", fmt.Errorf("%w: resolving harp %q: %v", ErrSharedScratchNoHarp, harp, err)
	}
	return dir, nil
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

// preferOutOfCwd is the SharedCell default-derivation step: a SHARED-cwd
// launch with no explicit per-surface preference should still prefer a
// race-safe form over the at-rest default (the native file) — that default
// exists for DeliverUnder, which has no argv sink for anything else, not for a
// launch, which does. The current selection is kept when the selected
// approach already has an OutOfCwd form; otherwise the kind's declared
// approaches are constructed and the one that has one is chosen.
//
// Whether an approach converts is read from the constructed VALUE
// (OutOfCwd), never from a name, so an engine's own naming decides nothing
// here. Two candidates is an ambiguity nothing can resolve silently: the
// default wins if it is one of them, otherwise the launch is refused naming
// both — a declaration that rich has to say which it prefers. No registered
// engine reaches that arm (TestApproachDispatch_SharedPreferenceIsUnambiguous).
//
// A candidate must ALSO be one this run can root (rootedInThisRun). Race-safety
// is a property of the approach; being deliverable is a property of the RUN,
// and this step used to read only the first. That is what let it hand Deliver
// a private-root approach on a run advising no private root, which then
// refused — the defect feeble-sway's acceptance failures were: an ordinary
// `ctxloom run`, and an agent run declaring engine_home: host, both stopped
// launching. keepOrReroot covers the case where nothing race-safe is rootable.
func (s *SurfaceSelection) preferOutOfCwd(kind SurfaceKind, in SurfaceInputs, start present.Start) error {
	cur, ok := s.names[kind]
	if !ok {
		return nil
	}
	p, ok := s.decl[kind]
	if !ok {
		return nil
	}
	if a, ok := p.Construct(cur, in, nil); ok {
		// BOTH conditions, and the second is why this is not just
		// SafeInSharedCwd: an approach that is race-safe but roots at a root
		// THIS RUN never advised cannot be delivered at all. Keeping it here
		// is what left delivery refusing a choice selection had made for it.
		if SafeInSharedCwd(a) && rootedInThisRun(a, start) {
			return nil
		}
	}
	var candidates, rootable []string
	for _, name := range p.Names() {
		a, ok := p.Construct(name, in, nil)
		if !ok {
			continue
		}
		// A Rider is race-safe but is not a SUBSTITUTE: it writes nothing of
		// its own, so deriving it here would silently move the surface onto a
		// different carrier the caller never named. Only an approach that
		// delivers its own bytes outside the project root is a candidate.
		if _, rider := a.(Rider); rider {
			continue
		}
		if !rootedInThisRun(a, start) {
			continue
		}
		rootable = append(rootable, name)
		if SafeInSharedCwd(a) {
			candidates = append(candidates, name)
		}
	}
	switch len(candidates) {
	case 0:
		return s.keepOrReroot(kind, p, cur, in, start, rootable)
	case 1:
		s.names[kind] = candidates[0]
		return nil
	default:
		for _, name := range candidates {
			if name == p.Default() {
				s.names[kind] = name
				return nil
			}
		}
		return fmt.Errorf("surface %s: %s declares more than one out-of-cwd approach (%s) and none of them is its default — the declaration must name the preferred one", kind, p.Engine(), strings.Join(candidates, ", "))
	}
}

// rootedInThisRun reports whether a's presentation lands beneath a root THIS
// RUN actually advised. It is the question selection was missing: preferOutOfCwd
// knew which approaches are race-safe in a shared cwd, but not which ones this
// particular run is equipped to deliver, so it promoted a surface to a
// private-root approach on a run that advised no private root and left Deliver
// to refuse the choice selection had just made (ErrUnrootedEngineHome on an
// ordinary `ctxloom run` — feeble-sway).
//
// It reads the PRESENTATION rather than a declared list of required roots,
// for the same reason PresentsUnderProjectRoot does: the presenter already
// states where the bytes go, and a marker list is a closed set a new approach
// silently falls outside of.
//
// Absolute-vs-relative is the whole test, and it is exact rather than a
// heuristic. Every root a run advises carries an ABSOLUTE host dir; a root it
// does not advise is the zero present.Root, whose Host is "". present.under
// joins the leaf onto that, so an approach built from an unadvised root yields
// a bare relative leaf (or "" when it roots at the root itself). There is no
// other way to reach a relative HostPath here.
func rootedInThisRun(a Approach, start present.Start) bool {
	host := a.Present(start).HostPath
	return host != "" && filepath.IsAbs(host)
}

// keepOrReroot runs when no race-safe approach can be rooted by this run.
//
// The historical behaviour was simply to keep the current selection, and that
// stays RIGHT whenever the current selection is one this run can deliver: a
// host run whose context surface is already the native project file keeps it,
// exactly as before.
//
// What is new is the other case. A kind whose DECLARED DEFAULT is itself a
// private-root approach (claude's MCP surface defaults to the private
// .mcp.json on every cell) would otherwise be kept on a run that cannot root
// it, and refuse at Deliver. Rerooting to an approach the run CAN deliver is
// what ErrUnrootedEngineHome's own message already advises a human to do by
// hand — "declare it on the binding, or select a project-file approach for
// this surface" — and there is no reason selection cannot do it.
//
// This is a change in WHICH approach a rootless run selects, never a
// degradation of one that was selected: a run that advises the private root
// still gets the private approach, which is what keeps the engine_home:
// session behaviour intact.
func (s *SurfaceSelection) keepOrReroot(kind SurfaceKind, p Presentations, cur string, in SurfaceInputs, start present.Start, rootable []string) error {
	if a, ok := p.Construct(cur, in, nil); ok && rootedInThisRun(a, start) {
		return nil
	}
	s.reroot(kind, p, rootable)
	return nil
}

// ensureRootable is the root-awareness that applies on EVERY cell, not just a
// shared one.
//
// preferOutOfCwd is a shared-cwd concern and is gated to CellKindShared, but
// "can this run deliver the approach it selected" is not about the cwd at all.
// An ISOLATED cell never runs the preference step, so it keeps each kind's
// DECLARED DEFAULT — and claude's MCP surface defaults to the private
// .mcp.json on every cell. A worktree run that advises no engine home
// therefore kept an approach it could not deliver and refused, with the engine
// never launching (the j002200 spy scenario, which runs under workspace
// "worktree").
//
// Rerouting to the well-known project file is not a loss of isolation here:
// an isolated cell's project root IS its private checkout, so the file lands
// inside the cell either way. That is the same reasoning the flip itself
// used — on an isolated cell a "private" root and the checkout are the same
// place — read in the other direction.
func (s *SurfaceSelection) ensureRootable(kind SurfaceKind, in SurfaceInputs, start present.Start) {
	p, ok := s.decl[kind]
	if !ok {
		return
	}
	cur, ok := s.names[kind]
	if !ok {
		return
	}
	if a, ok := p.Construct(cur, in, nil); ok && rootedInThisRun(a, start) {
		return
	}
	var rootable []string
	for _, name := range p.Names() {
		a, ok := p.Construct(name, in, nil)
		if !ok {
			continue
		}
		// Same reason preferOutOfCwd skips it: a Rider writes nothing of its
		// own, so choosing one here would move the surface onto a carrier the
		// caller never named.
		if _, rider := a.(Rider); rider {
			continue
		}
		if rootedInThisRun(a, start) {
			rootable = append(rootable, name)
		}
	}
	s.reroot(kind, p, rootable)
}

// reroot picks among the approaches this run CAN root, preferring the kind's
// declared default. An empty list leaves the selection alone: nothing here can
// be delivered, so Deliver refuses with its own root-specific remedy naming
// the missing root, and inventing a choice would only obscure that.
func (s *SurfaceSelection) reroot(kind SurfaceKind, p Presentations, rootable []string) {
	switch len(rootable) {
	case 0:
		return
	case 1:
		s.names[kind] = rootable[0]
	default:
		for _, name := range rootable {
			if name == p.Default() {
				s.names[kind] = name
				return
			}
		}
		s.names[kind] = rootable[0]
	}
}

// deliverSet selects from the engine's Declaration, constructs the selected
// approaches from in, and delivers each through the cell named by
// req.CellKind, recording each returned handle so Cleanup can reverse it
// (LIFO). A SharedCell takes the race-safe set (out-of-cwd flag files, or a
// warned Unsafe well-known write) prepared for the live working dir; an
// isolated cell takes the plain well-known set written into its private
// directory (the working dir, which the worktree/container private-checkout
// makes non-racy).
//
// Hook-carried context (a context approach declaring Rider) is installed
// HERE, on every cell: the cache file is materialized and the injection hook
// appended to the merged hooks the not-yet-delivered settings surface then
// writes. It used to be installed only on the SharedCell arm, so a worktree or
// container launch pinned to the hook approach wrote the hook nowhere and
// launched a context-less session while Setup reported success.
//
// A failed delivery REFUSES and names the surface; it is never converted into
// a different mechanism. This used to fall back to the SessionStart injection
// hook when the context surface failed, which delivered the run its context
// through a channel its isolation argument was never made against and still
// reported success (feeble-sway).
// req is NON-NIL: the sole caller has already dereferenced it.
func (b *LaunchBackend) deliverSet(in SurfaceInputs, req *SetupRequest, start present.Start) error {
	// Launch delivers the WHOLE surface set, so it drives the builder over the same
	// full selection materialize/apply use — the builder is the single selection
	// input everywhere. Launch KEEPS its own cell machinery: only the surface
	// LIST is derived from the selection, not the cell/placement choice.
	sel := Select(b.surfaces).WithEverything()
	explicit := map[SurfaceKind]bool{}
	for kind := range req.Managed.Surfaces {
		explicit[kind] = true
	}
	// The shared-cwd PREFERENCE is restricted to CellKindShared (an isolated
	// cell's well-known write is already race-free by construction — see
	// Deliveries' doc — so there is nothing to prefer). ROOT-AWARENESS is not:
	// "can this run deliver what it selected" is true of every cell, and an
	// isolated cell that never runs the preference step keeps each kind's
	// declared default — which for claude's MCP surface is the private
	// .mcp.json, unrootable on a run advising no engine home.
	//
	// Both are skipped for any kind the caller named explicitly
	// (req.Managed.Surfaces, applied below): an explicit context=unsafe-file
	// preference must be HONORED, not silently converted back to the scratch.
	for kind := range sel.names {
		if explicit[kind] {
			continue
		}
		if req.CellKind == CellKindShared {
			if err := sel.preferOutOfCwd(kind, in, start); err != nil {
				return err
			}
			continue
		}
		sel.ensureRootable(kind, in, start)
	}
	// The agent binding's preference, applied where it is actually valid: a
	// launch has the argv sink a flag-announced approach needs, which is
	// exactly why this belongs on the agent rather than in the engine's
	// declaration (an at-rest DeliverUnder inheriting it would fail for want
	// of one).
	for kind, name := range req.Managed.Surfaces {
		sel = sel.With(kind, name)
	}
	resolved, err := sel.Build(in, nil)
	if err != nil {
		return err
	}
	b.resolved = resolved

	// LaunchFormPresent: this run rides a session another run set up. The
	// selection is built identically — same inputs, same preference, same
	// approaches — so each surface resolves to the same place it would have
	// been delivered; what changes is that this run NAMES those places instead
	// of writing them. See presentExisting for the one rule that decides which
	// is which, and why a missing surface refuses rather than falling back.
	if req.Form == LaunchFormPresent {
		return b.presentExisting(resolved, start)
	}

	// installHook: a resolved context approach that RIDES the hooks surface
	// carries nothing itself — the launch installs the hook it rides on. Same
	// on every cell.
	installHook := func(rs resolvedSurface) error {
		if rs.kind != SurfaceContext {
			return nil
		}
		if _, rider := rs.approach.(Rider); !rider {
			return nil
		}
		if !b.installContextInjectionHook(report.To(req.Reporter), req) {
			return fmt.Errorf("failed to install the context-injection hook for surface %s", rs.kind)
		}
		return nil
	}

	if req.CellKind == CellKindShared {
		for _, rs := range resolved.surfaces {
			d, err := resolved.deliverOneShared(rs, start)
			if err != nil {
				// A failed context delivery is a REFUSAL, not an invitation to
				// try a different approach. Silently switching to the injection
				// hook here delivered context the run never asked for, through a
				// mechanism its isolation argument was not made against — the
				// cross-approach fallback the design forbids. The run stops and
				// says which surface failed instead.
				return fmt.Errorf("failed to deliver surface into shared cwd: %w", err)
			}
			if err := installHook(rs); err != nil {
				return err
			}
			if d != nil { // a no-op delivery (wrote nothing) holds no cleanup handle
				b.delivered = append(b.delivered, d)
			}
		}
		return nil
	}

	cell := NewIsolatedCell(start)
	for _, rs := range resolved.surfaces {
		d, err := cell.Deliver(rs.approach)
		if err != nil {
			return fmt.Errorf("failed to deliver surface: %w", err)
		}
		if err := installHook(rs); err != nil {
			return err
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
// flag-context backend (claude), and it runs on ONE path only: a
// deliberately-selected ApproachHook context surface (a documented no-op WRITE
// — the Rider, HookCarriedContext — that otherwise installs nothing at all).
// It is never reached as a fallback from another approach's failure; a failed
// delivery refuses the launch. It appends ONLY the injection hook
// (never re-runs MergeManaged, which would clobber the statusline state).
// Reports whether the install took hold.
func (b *LaunchBackend) installContextInjectionHook(rep report.Reporter, req *SetupRequest) bool {
	if err := b.context.Provide(b.WorkDir(), req.Fragments); err != nil {
		rep.Warnf("context-injection hook install: Provide failed: %v", err)
		return false
	}
	hash := b.context.GetContextHash()
	if hash == "" {
		return true // empty context: nothing to inject, and nothing was lost.
	}
	hooks, _, ok := b.mergedState()
	if !ok || hooks == nil {
		rep.Warnf("context-injection hook install: could not read the merged hooks state")
		return false
	}
	hooks.Unified.SessionStart = append(hooks.Unified.SessionStart,
		NewContextInjectionHooks(rep, hash, b.WorkDir())...)
	return true
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
// that delivered nothing (the legacy lifecycle path, the minimal form, or a
// run presenting the session's surfaces) holds no handles, so this is a no-op
// there.
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

// presentExisting is LaunchFormPresent's delivery: leave on disk the files this
// run will NAME on argv, without taking ownership of anything another run owns.
//
// ONE RULE, and it is a property of the approach rather than of this caller. An
// approach implements Existing when its out-of-cwd form can be located without
// being written; how it then satisfies the rule follows from how its file is
// NAMED, which is the approach's own decision:
//
//	FIXED NAME       (settings.json beneath the session scratch, .mcp.json
//	                 beneath the session's engine home) —
//	                 owned by whichever run wrote it, and every run in the
//	                 session reads that one copy. The only non-clobbering way to
//	                 satisfy the rule is to REQUIRE it: present it when it is
//	                 there, and REFUSE (ErrAbsentSharedSurface) when it is not.
//	                 Writing it would clobber the session's; re-injecting its
//	                 content by a second route is the silent degrade this form
//	                 exists to remove. There is no third option, and that is the
//	                 point.
//	NAMED BY BYTES   (the framed context file, <sha256-prefix>.sysprompt.md) —
//	                 owned by nobody. Two different contents are two different
//	                 files, identical content is identical bytes, so writing it
//	                 can neither clobber another run's nor degrade this one's.
//	                 It satisfies the rule by writing.
//
// An approach that does NOT implement Existing contributes nothing: it has no
// out-of-cwd form to name, its well-known file is the session's own, and this
// run neither writes it nor announces it. So emission and writing never drift
// apart — a surface this run did not put on disk is a surface it does not put
// on argv.
//
// No handle is recorded on any path. Either the file was already there, or it
// is content-addressed and shared by every run with the same bytes; retracting
// a surface the session still needs is the one failure worse than not having
// presented it.
func (b *LaunchBackend) presentExisting(resolved *ResolvedSelection, start present.Start) error {
	for _, rs := range resolved.surfaces {
		e, ok := rs.approach.(Existing)
		if !ok {
			continue
		}
		if _, err := e.PresentExisting(start); err != nil {
			return fmt.Errorf("failed to present the session's %s surface: %w", rs.kind, err)
		}
	}
	return nil
}
