package operations

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/transcript"
)

// RunOneshotRequest specifies a single profile-agent oneshot run: assemble a
// profile's context, launch its backend once, and capture stdout. `ctxloom acp
// client` and the init auth-ping both build on it directly; a delegated
// child's oneshot fallback and `ctxloom run --print` mirror the same tail
// (runResolvedAgent) without going through this facade.
type RunOneshotRequest struct {
	Profile   string // profile whose context specializes this agent (may be empty)
	Task      string // the prompt/task sent to the agent
	LLM       string // optional label/backend override; wins over the profile's llm
	WorkDir   string // working directory for the run
	Verbosity int

	// Permissions is an explicit posture override, in the same string
	// vocabulary agent.ParsePermissionMode accepts (agent.PermissionBypass.
	// String(), etc.). It wins over the resolved label's configured
	// permissions — for a caller whose intent about gating does not come
	// from an llm label at all (the init auth-ping is the first such caller:
	// a fixed trivial probe that wants no permission gating, unrelated to
	// whatever posture the chosen engine's label happens to declare). Empty
	// defers to the label's configured posture, unchanged from before this
	// field existed.
	Permissions string

	// Pipeline is an optional pre-configured process stage (test seam).
	Pipeline *bundles.Pipeline
	// Factory builds the plugin client; nil self-invokes the compiled-in
	// backend carrying the resolved config label
	// (pb.DefaultClientFactoryForLabel). The seam lets delegated agent_run
	// children and tests inject a client without spawning real backends.
	Factory pb.ClientFactory
}

// RunOneshotResult is the captured output of one oneshot agent run, plus the
// resolved transport metadata.
type RunOneshotResult struct {
	Profile string `json:"profile,omitempty"`
	Output  string `json:"output"`
	Label   string `json:"label"`
	Backend string `json:"backend"`
	Model   string `json:"model,omitempty"`
}

// RunOneshot assembles the profile's context, resolves the LLM (override → the
// profile's declared llm → primary role), and runs the backend once in ONESHOT
// mode with stdout captured. It mirrors memory/compactor's distillation run: the
// client factory abstracts backend construction, the model rides in RunOptions,
// and SkipSetup keeps startup minimal (no hooks/MCP/statusline) — the profile's
// assembled context is the only specialization.
func RunOneshot(ctx context.Context, cfg *config.Config, req RunOneshotRequest) (*RunOneshotResult, error) {
	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profile:  req.Profile,
		Pipeline: req.Pipeline,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	label := resolveOneshotLabel(cfg, req.LLM, ctxResult.ProfileLLM)
	backendName, model := ResolveBackend(cfg, label)
	labelEntry, _ := cfg.GetLLMEntry(label)
	permissions := req.Permissions
	if permissions == "" {
		permissions = labelEntry.Permissions
	}

	// The single-profile oneshot's axes: the session-level workspace default
	// (cfg.Workspace) x the project runtime default (cfg.Runtime — a bare
	// profile has no agent binding to declare one). Build the shared
	// executable trust gate ONLY when some isolation is actually requested (an
	// all-defaults oneshot writes no per-member config and must stay
	// byte-identical to pre-P3; gate construction runs the trust baseline +
	// opens the store). Ignored on the injected-Factory path.
	axes := isolation.Axes{Workspace: isolation.WorkspaceAxis(cfg.GetWorkspace()), Runtime: isolation.RuntimeAxis(cfg.GetRuntime())}
	// The all-defaults member writes no per-member config, so nothing consults
	// this gate at all — AdmitAll states that, where a nil would claim the gate
	// was forgotten and withhold if anything ever did consult it.
	var execGate *ExecutableTrustGate
	gate := bundles.AdmitAll()
	if !axes.Zero() {
		execGate = NewExecutableTrustGate(cfg)
		gate = execGate.Authorizer()
	}
	// Surface (content-free) whatever that gate withheld from the member's
	// per-member config, exactly as MaterializeProfile and ApplyHooks do: a
	// withhold must never be silent or reasonless (docs/trust-model.md).
	// Deferred so it reports on the failure path too, and nil-safe for the
	// all-defaults oneshot that builds no gate at all.
	defer execGate.WarnWithheld()
	// The single-profile oneshot's profile set is just req.Profile (empty falls back
	// to the configured defaults inside AssembleManagedConfig), matching how the
	// assembled context above was scoped.
	var profiles []string
	if req.Profile != "" {
		profiles = []string{req.Profile}
	}

	res, err := runResolvedAgent(ctx, resolvedRunRequest{
		Context:   ctxResult.Context,
		Task:      req.Task,
		WorkDir:   req.WorkDir,
		Verbosity: req.Verbosity,
		Label:     label,
		Backend:   backendName,
		Model:     model,
		// A bare-profile oneshot has no agent binding; its posture is the
		// caller's explicit override if it gave one, else the engine label's
		// configured permissions (if any) — resolved for headless below.
		Permissions: permissions,
		// AgentID scopes a per-agent workspace by the profile name.
		Axes:           axes,
		IsolationImage: IsolationImageConfig(cfg, backendName),
		AgentID:        req.Profile,
		Profiles:       profiles,
		Gate:           gate,
		Factory:        req.Factory,
	})
	if err != nil {
		return nil, err
	}
	res.Profile = req.Profile
	return res, nil
}

// resolvedRunRequest is an already-resolved agent run: a composed context and the
// transport it resolved to. It is the seam shared by RunOneshot (which resolves a
// single profile) and a delegated child's oneshot fallback (PrepareAgentChat's
// startOneshot), so the backend-launch tail is written once.
type resolvedRunRequest struct {
	Context   string // assembled context injected as the agent's lead fragment
	Task      string // the prompt/task sent to the agent
	WorkDir   string
	Verbosity int

	// Label/Backend/Model are the already-resolved transport (the label drives
	// the plugin, the model rides in RunOptions).
	Label   string
	Backend string
	Model   string

	// Permissions is the member's declared posture (agent binding or label
	// config; "" = none). The fan is always headless ONESHOT, so it is resolved
	// to an effective posture in runResolvedAgent: an honorable read-only plan is
	// kept, a would-block posture floors to bypass so a member can't hang.
	Permissions string

	// Axes is the resolved isolation request: the session-supplied workspace
	// axis x the agent-resolved runtime axis (both already defaulted). It
	// selects HOW the member's plugin is spawned and WHERE its workspace lives
	// when no Factory is injected. IsolationImage carries the user's
	// container-image configuration for the member's backend (config
	// isolation_images — run as-is — and isolation_base_containerfile for
	// local builds); the zero value keeps the backend's built-in defaults.
	// AgentID scopes/names that per-agent workspace (the member identifier).
	// All are ignored on the injected-Factory path.
	Axes           isolation.Axes
	IsolationImage isolation.ImageConfig
	AgentID        string

	// Profiles is the member's resolved profile set — the SAME set that scoped its
	// assembled Context. When the member's workspace is ISOLATED (worktree/
	// container), it scopes the per-member ManagedConfig (mcp/hooks/commands) written
	// natively into the isolated cwd, mirroring the top-level run. Ignored for a
	// none member (which shares the project cwd and writes no managed config).
	Profiles []string
	// Gate is the shared executable trust gate (built ONCE per fan) threaded into
	// the isolated member's ManagedConfig assembly, so bundle MCP/hooks/command
	// exports gate at their own choke exactly as the top-level run.
	// bundles.AdmitAll = deliberately no gating (and none members never consult it).
	Gate bundles.Authorizer

	// ExtraEnv is merged over the workspace env into the member engine's
	// environment (a delegated child's session harp / bus socket / depth).
	ExtraEnv map[string]string

	Factory pb.ClientFactory // nil self-invokes the compiled-in backend
}

// IsolationImageConfig assembles the user's container-image configuration for
// a backend's isolated runs: the per-backend prebuilt-image override (config
// isolation_images), the base Containerfile local builds layer the agent
// stage onto (config isolation_base_containerfile), the project root
// devcontainer auto-detection resolves against + its opt-out/service pick,
// and the composable engine set (isolation_engines).
func IsolationImageConfig(cfg *config.Config, backend string) isolation.ImageConfig {
	if cfg == nil {
		return isolation.ImageConfig{}
	}
	return isolation.ImageConfig{
		Image:               cfg.IsolationImageFor(backend),
		BaseContainerfile:   cfg.IsolationBaseContainerfilePath(),
		AppRoot:             cfg.GetAppRoot(),
		NoDevcontainerBase:  !cfg.IsolationDevcontainerBaseEnabled(),
		DevcontainerService: cfg.GetIsolationDevcontainerService(),
		Engines:             cfg.GetIsolationEngines(),
	}
}

// CellKindForPolicy maps a resolved isolation.Policy to the agent.CellKind the
// host stamps onto RunOptions.CellKind, so the plugin learns which cell it runs
// in (it can't infer that from WorkDir alone). It lives HERE — at the run
// boundary where both isolation and agent are already imported — rather than in
// isolation, so isolation need not depend on agent. None → Shared, Worktree →
// DirectoryIsolated, and either container base (container / container-worktree)
// → ProcessIsolated. Both cli/run.go and oneshot.go stamp through this one map.
func CellKindForPolicy(p isolation.Policy) agent.CellKind {
	switch {
	case isolation.IsContainerPolicyName(p.Name()):
		return agent.CellKindProcessIsolated
	case isolation.Isolated(p): // a worktree (isolated, but not container)
		return agent.CellKindDirectoryIsolated
	default: // none — the shared live project dir
		return agent.CellKindShared
	}
}

// mcpCommandOverrider is the narrow capability isolation.Container implements
// (MCPCommandOverride) and None/Worktree do not — probed here rather than
// added to the isolation.Policy interface so the two policies that carry no
// override need no method at all.
type mcpCommandOverrider interface {
	MCPCommandOverride() string
}

// MCPCommandOverrideForPolicy mirrors CellKindForPolicy: it reports the
// container axis's MCP command override for a resolved policy — "" for
// none/worktree (no override; the host self-exec-absolute invariant in
// agent.CtxloomCommand applies unchanged), or the in-container ctxloom binary
// path for a container policy (host or worktree base) — so the host can stamp
// it onto the run env (agent.MCPCommandOverrideEnv) and the in-container
// MCP-surface writer emits the CONTAINER path instead of the host self-exec
// path. Lives HERE, not in isolation, for the same reason
// CellKindForPolicy does: this is the run boundary where both isolation and
// agent are already imported.
func MCPCommandOverrideForPolicy(p isolation.Policy) string {
	if o, ok := p.(mcpCommandOverrider); ok {
		return o.MCPCommandOverride()
	}
	return ""
}

// runtimeCarrier / containerPersister are the narrow capabilities the container
// policy implements (Runtime / ContainerPersistDir) and None/Worktree do not —
// probed here rather than widening isolation.Policy, mirroring
// mcpCommandOverrider. They feed the docker-exec interactive launcher (Phase
// 2a-A): the runtime renders `exec -it`, and the container persist dir is where
// the in-container turn reads the RunStart handoff.
type runtimeCarrier interface{ Runtime() isolation.Runtime }
type containerPersister interface{ ContainerPersistDir(harp string) string }

// RuntimeForPolicy reports a container policy's launch runtime (docker/podman),
// or nil for none/worktree — the seam the docker-exec vpio.Launcher renders
// `exec -it` through.
func RuntimeForPolicy(p isolation.Policy) isolation.Runtime {
	if rc, ok := p.(runtimeCarrier); ok {
		return rc.Runtime()
	}
	return nil
}

// ContainerPersistDirForPolicy reports the IN-CONTAINER path the host's session
// persist dir is bind-mounted to for a container policy (where the docker-exec
// turn reads the RunStart handoff), or "" for none/worktree.
func ContainerPersistDirForPolicy(p isolation.Policy, harp string) string {
	if cp, ok := p.(containerPersister); ok {
		return cp.ContainerPersistDir(harp)
	}
	return ""
}

// prepareIsolation is runResolvedAgent's seam onto isolation.Prepare — a
// package var so tests simulate a container degrade (which records
// ClassIsolation findings) without probing the real host's container
// runtimes. Mirrors isolation's selectRuntimeProbe/sharedFSCheck seam style.
var prepareIsolation = isolation.Prepare

// isolationGateErr is the fail-loudly member gate over the strictness findings
// collected during one member's isolation.Prepare: a ClassIsolation finding
// means an explicitly-requested isolation guarantee could not be satisfied AS
// REQUESTED and the prepared workspace silently degraded toward a WEAKER
// posture than asked for. Two distinct cases share this class today: (1) a
// requested CONTAINER couldn't start and the run fell back to the bare,
// unsandboxed host; (2) a requested WORKTREE's per-agent config-home has no
// credentials to seed and no API-key env, so the engine would launch logged
// out. Either way, running the member as-is would silently
// deliver less than what was asked for. In strict mode that fails THE MEMBER
// (an error Part in the fan; other members continue — partial success is
// still success), never the whole call. Returns nil in degraded mode
// (recording is disabled there, so found is empty anyway) or when no
// ClassIsolation finding was collected. The finding's own Message fully
// describes WHICH case fired — this wrapper adds no case-specific wording, so
// it never misdescribes one case using the other's vocabulary.
func isolationGateErr(found []strictness.Finding) error {
	if strictness.Degraded() {
		return nil
	}
	var iso []strictness.Finding
	for _, f := range found {
		if f.Class == strictness.ClassIsolation {
			iso = append(iso, f)
		}
	}
	if len(iso) == 0 {
		return nil
	}
	var b strings.Builder
	b.WriteString("isolation: refusing to run this member — an explicitly-requested isolation guarantee could not be satisfied")
	for _, f := range iso {
		fmt.Fprintf(&b, ": %s", f.Message)
	}
	if fix := iso[0].FixIt; fix != "" {
		fmt.Fprintf(&b, " (fix: %s)", fix)
	}
	return errors.New(b.String())
}

// memberLabel names the run in a diagnostic: the member/agent identifier when
// one exists (the fan and delegated children set AgentID), else the resolved
// engine label, else the backend. Every caller of runResolvedAgent supplies at
// least a backend, so this never returns "".
func memberLabel(req resolvedRunRequest) string {
	switch {
	case req.AgentID != "":
		return req.AgentID
	case req.Label != "":
		return req.Label
	default:
		return req.Backend
	}
}

// effectiveMemberPermission resolves the posture a fan member actually launches
// with. Fan-out is ALWAYS non-interactive ONESHOT: there is no human to answer
// the engine's prompt. An honorable read-only plan (on a backend that enforces
// it) is kept; any posture that is not SafeHeadless — default/acceptEdits, an
// unset/empty declaration, or plan on a backend with no read-only tier —
// REFUSES rather than hanging. It used to floor a would-block posture up to
// bypass instead: a member that declared nothing, or declared a plan its
// backend cannot enforce, silently ran at full bypass with no warning
// anywhere — the same silent-elevation shape as the ONE-SHOT ARM bug this
// refusal closes, just reached through the fan/delegated-child path instead
// of `acp run --one-shot`. Elevating to the most permissive setting because a
// posture could not be honoured headless is worse than refusing.
//
// A posture the parser does not RECOGNISE is a different input from an unset
// one, even though both parse to PermissionDefault. Unset declares that nothing
// was declared; a misspelling is a declaration that MISSED. Both are refused
// now, but with distinct error text, so a misspelling that would have silently
// become the most permissive setting is still named as what it is (a typo),
// not folded into the generic headless refusal. LLMEntry.Permissions arrives
// straight from a hand-edited config.yaml with no validation on the way in, so
// that input is reachable.
func effectiveMemberPermission(req resolvedRunRequest) (agent.PermissionMode, error) {
	mode, known := agent.ParsePermissionMode(req.Permissions)
	if !known && strings.TrimSpace(req.Permissions) != "" {
		return mode, fmt.Errorf("unknown permission mode %q for %q: expected one of %s (an unrecognised posture would run as %q, the most permissive setting)",
			req.Permissions, memberLabel(req), strings.Join(agent.PermissionModeNames(), ", "), agent.PermissionBypass)
	}
	mode = mode.CollapsePlanIfUnenforced(backends.EnforcesReadOnlyPlan(req.Backend))
	if !mode.SafeHeadless() {
		return mode, fmt.Errorf("permission mode %q for %q cannot run headless: a oneshot has no human to answer an engine permission prompt, so honouring it would hang — and silently elevating it to %q instead would be worse; declare %q or a backend-enforced %q",
			mode, memberLabel(req), agent.PermissionBypass, agent.PermissionBypass, agent.PermissionPlan)
	}
	return mode, nil
}

// runResolvedAgent launches the resolved backend once in ONESHOT mode with the
// composed context as the lead fragment and stdout captured. It carries no
// context-assembly or LLM-resolution logic — those happen upstream (RunOneshot
// for a single profile, ResolveAgent for an agent/bare-profile member) — so
// the two paths share one backend-launch tail and can never drift.
func runResolvedAgent(ctx context.Context, req resolvedRunRequest) (*RunOneshotResult, error) {
	// Naming a specialisation and delivering none of it is never what
	// the caller asked for. `if req.Context != ""` alone would launch the engine
	// unspecialised — it still runs and still produces plausible output, so the
	// loss is invisible at every consumer. The discriminator is whether a profile
	// set was NAMED: a bare `run --print` / defaults-only agent has no profiles
	// and is legitimately context-free (below), while a member whose named
	// profiles assembled to nothing is a failed assembly and must say so.
	if strings.TrimSpace(req.Context) == "" && len(req.Profiles) > 0 {
		return nil, fmt.Errorf("empty context: profile set %v assembled to nothing — refusing to run %q unspecialised (check the profile's fragments/bundles resolve, or drop the profile to run context-free)",
			req.Profiles, memberLabel(req))
	}
	// Resolve the effective posture before any workspace is prepared, so a
	// declaration that missed costs nothing and blocks everything.
	memberPerm, err := effectiveMemberPermission(req)
	if err != nil {
		return nil, err
	}

	var fragments []*pb.Fragment
	if req.Context != "" {
		fragments = append(fragments, &pb.Fragment{Content: req.Context})
	}

	// Decide WHERE this member runs and HOW its plugin is spawned. An injected
	// Factory (test seam / caller override) wins exactly as before: the isolation
	// policy is skipped entirely, WorkDir stays req.WorkDir, and no workspace is
	// prepared or torn down — byte-identical to the pre-isolation path. Only when
	// Factory is nil does the resolved policy take over. Default (empty/none) →
	// the live project dir + a bare self-invoked subprocess (== the old
	// pb.DefaultClientFactory), so this is zero functional change until an
	// isolation is actually requested.
	factory := req.Factory
	workDir := req.WorkDir
	var workspaceEnv map[string]string
	// A none member (or the injected-Factory test path) keeps the pre-P3 delivery:
	// SkipSetup:true, no managed write, context as the lead fragment. An ISOLATED
	// member flips to SkipSetup:false + a per-member ManagedConfig written into its
	// isolated cwd (set below).
	skipSetup := true
	var managed *pb.ManagedConfig
	// Resolved isolation cell stamped onto RunOptions below so the plugin knows
	// which cell it runs in. Defaults to Shared: the injected-Factory test path
	// (and a none member) share the live cwd; the isolation branch overwrites it
	// from the resolved policy. Setup does not consume it yet (plan S4b).
	cellKind := agent.CellKindShared
	if factory == nil {
		// Member isolation. Prepare realizes the per-axis degrade chain: a
		// runtime-axis failure drops only the container dimension (a requested
		// worktree survives); a workspace-axis failure degrades worktree→none.
		// It warns at each degrade and never blocks — None never fails. The
		// none tier loses cwd config isolation (shared project dir), the
		// documented non-git edge.
		//
		// Fail-loudly member gate: a container degrade inside Prepare records a
		// ClassIsolation finding, but the fan has no startup choke owner to abort
		// on it — and the headless floor below would then run this member on the
		// bare host with FORCED BYPASS. Checkpoint before Prepare and gate on the
		// findings it recorded, failing THIS MEMBER (an error Part upstream;
		// other members continue) unless --degraded. Parallel members no longer
		// need external serialization here — strictness gives each goroutine's
		// window its OWN findings log; Close releases this
		// goroutine's registry entry once the window is read so a long fan
		// (or a long-lived host process running many fans) doesn't accumulate
		// one entry per member forever.
		mark := strictness.Checkpoint()
		policy, ws := prepareIsolation(ctx, req.Axes, req.Backend, req.IsolationImage, req.WorkDir, req.AgentID, isolation.SessionStateFromEnv(req.ExtraEnv))
		found := strictness.Since(mark)
		strictness.Close(mark)
		workDir = ws.Dir()
		// Per-agent config-home envs (worktree) isolate each engine's GLOBAL
		// config layer; nil for none/container. Threaded into the member's engine
		// env below so the shared ~/.claude.json etc. don't clobber.
		workspaceEnv = isolation.WorkspaceEnv(ws)
		// Tear the workspace down after the run. Registered BEFORE the client so
		// it runs AFTER client.Kill() (kill the plugin before removing its
		// workspace — WIP-safe for the worktree teardown). none's cleanup is a
		// noop. Registered before the gate below, so a gated member's degraded
		// workspace is still torn down. The error is deliberately dropped: a
		// cleanup failure surfaces from INSIDE Cleanup (a streamed warning
		// naming the residue path + fix — see warnCleanupResidue), and it must
		// NOT become a finding here — this defer fires outside the serialized
		// gate window, where a recorded finding would poison a concurrent
		// member's gate.
		defer func() { _ = ws.Cleanup() }()
		if gerr := isolationGateErr(found); gerr != nil {
			return nil, gerr
		}
		// Oneshot/fan members have no coordinator reach-back by design (their
		// output is bridged at the boundary), so no per-spawn runner env.
		factory = isolation.FactoryForWorkspace(policy, ws, nil)
		// Stamp the resolved cell (none→Shared, worktree→DirectoryIsolated,
		// container→ProcessIsolated) — set unconditionally from the actual policy,
		// not gated on Isolated, so a none member is explicitly Shared too. This
		// complements the SkipSetup-as-proxy below (which S4b will supersede).
		cellKind = CellKindForPolicy(policy)

		// P3 write-enable: an ISOLATED member gets per-member NATIVE config written
		// into its isolated cwd (the point of the worktree, plan §2b). Assemble it
		// exactly as the top-level run does (backends.AssembleManagedConfig with the
		// SAME workDir/gate/profiles) so the plugin's Setup materializes
		// .mcp.json/.claude/AGENTS.md/.kiro/ into ws.Dir() and delivers context ONCE
		// from the lead fragment — mirroring run.go's SkipSetup:false delivery. A
		// none member (Isolated == false, incl. a worktree that degraded to none)
		// shares the project cwd, so it stays on the SkipSetup:true / lead-fragment
		// path: writing per-member config there would clobber the one shared surface.
		if isolation.Isolated(policy) {
			skipSetup = false
			managed = pb.ManagedConfigToProto(
				backends.AssembleManagedConfig(req.Backend, workDir, req.Gate, req.Profiles))
		}
	}

	env := workspaceEnv
	if len(req.ExtraEnv) > 0 {
		merged := make(map[string]string, len(workspaceEnv)+len(req.ExtraEnv))
		maps.Copy(merged, workspaceEnv)
		maps.Copy(merged, req.ExtraEnv)
		env = merged
	}

	runReq := &pb.RunStart{
		Fragments: fragments,
		Prompt:    &pb.Fragment{Content: req.Task},
		Options: &pb.RunOptions{
			WorkDir:        workDir,
			Env:            env,
			PermissionMode: memberPerm.String(),
			Mode:           pb.ExecutionMode_ONESHOT,
			Model:          req.Model,
			Verbosity:      agent.WireVerbosity(req.Verbosity),
			SkipSetup:      skipSetup,
			CellKind:       pb.CellKindToProto(cellKind),
		},
		ManagedConfig: managed,
	}

	client, err := factory(req.Backend, req.Label, req.Verbosity)
	if err != nil {
		return nil, fmt.Errorf("start plugin: %w", err)
	}
	defer client.Kill()

	var stdout, stderr bytes.Buffer
	exitCode, err := client.Run(ctx, runReq, nil, &stdout, &stderr, nil)
	if err != nil {
		return nil, fmt.Errorf("agent run: %w", err)
	}
	// Every non-zero exit returns an error above, so RunOneshotResult is only
	// ever constructed for exitCode==0 — it used to carry that (always-0)
	// value as an ExitCode field nothing read; deleted.
	if exitCode != 0 {
		return nil, fmt.Errorf("agent exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}

	// A oneshot exists ONLY to capture output — this tail is shared by
	// a delegated oneshot turn and `acp run` (and mirrored by `run --print`),
	// and every one of them publishes res.Output as the run's whole product
	// (a child's assistant SessionEntry, RunOneshotResult.Output). Exit 0
	// with zero bytes is therefore never "nothing to do, legitimately": it is an
	// engine that failed to answer while claiming success, and it used to reach
	// the user as an empty report with no error anywhere. Fail loudly and carry
	// stderr, which is where such an engine usually explains itself.
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		msg := fmt.Sprintf("agent produced no output: %q exited 0 with an empty stdout", memberLabel(req))
		if e := strings.TrimSpace(stderr.String()); e != "" {
			msg += ": " + e
		}
		return nil, errors.New(msg)
	}

	// S6 oneshot capture: this Execute call returns prose on stdout with no
	// ChatEvent stream, so the structured tee (GRPCClient.Chat/coord's
	// enginehost.startRun, S2) never fires for it — see
	// transcript.RecordOneshot's doc. The harp rides req.ExtraEnv exactly
	// like the delegated-child structured path (agent.SessionHarpEnv,
	// stamped by coord/children.go's childEnv); RunOneshot's own direct
	// callers (`acp run`, the init auth-ping) carry no harp and degrade to
	// RecordOneshot's own empty-harp no-op. Best-effort: a capture failure
	// warns but must never fail an otherwise-successful run.
	if harp := req.ExtraEnv[agent.SessionHarpEnv]; harp != "" {
		if terr := transcript.RecordOneshot(harp, req.Backend, req.Task, stdout.String()); terr != nil {
			clidiag.Warn("ctxloom", "oneshot transcript capture: %v", terr)
		}
	}

	return &RunOneshotResult{
		Output:  out,
		Label:   req.Label,
		Backend: req.Backend,
		Model:   req.Model,
	}, nil
}

// ResolveBackend maps a config label to its backend type and model. A label that
// is not a configured entry but names a registered backend type resolves to that
// backend directly (the ad-hoc `--llm <type>` convenience); otherwise
// cfg.ResolveLLM's lookup/default applies. Shared by `run` and the
// oneshot/agent_run path so backend resolution is identical everywhere.
func ResolveBackend(cfg *config.Config, label string) (backend, model string) {
	backend, model = cfg.ResolveLLM(label)
	if _, configured := cfg.GetLLMEntry(label); !configured && backends.Exists(label) {
		return label, ""
	}
	return backend, model
}

// resolveOneshotLabel picks the config label for a oneshot run: an explicit
// override wins, then the profile's declared llm, then the primary role. Unknown
// labels degrade through cfg.ResolveLLM (→ default backend), so a stale profile
// llm never blocks the run (CLAUDE.md fault tolerance).
func resolveOneshotLabel(cfg *config.Config, override, profileLLM string) string {
	if override != "" {
		return override
	}
	if profileLLM != "" {
		return profileLLM
	}
	return cfg.PrimaryLabel()
}
