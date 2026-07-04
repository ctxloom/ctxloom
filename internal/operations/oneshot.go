package operations

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// RunOneshotRequest specifies a single profile-agent oneshot run: assemble a
// profile's context, launch its backend once, and capture stdout. It is the
// backbone both `ctxloom run --print` and the parallel `map`/`weave` orchestration
// build on, so a member agent is just one of these.
type RunOneshotRequest struct {
	Profile   string // profile whose context specializes this agent (may be empty)
	Task      string // the prompt/task sent to the agent
	LLM       string // optional label/backend override; wins over the profile's llm
	WorkDir   string // working directory for the run
	Verbosity int

	// Loader is an optional pre-configured bundle loader (test seam).
	Loader *bundles.Loader
	// Factory builds the plugin client; nil self-invokes the compiled-in
	// backend carrying the resolved config label
	// (pb.DefaultClientFactoryForLabel). The seam lets map/weave and tests
	// inject a client without spawning real backends.
	Factory pb.ClientFactory
}

// RunOneshotResult is the captured output of one oneshot agent run, plus the
// resolved transport metadata.
type RunOneshotResult struct {
	Profile  string `json:"profile,omitempty"`
	Output   string `json:"output"`
	Label    string `json:"label"`
	Backend  string `json:"backend"`
	Model    string `json:"model,omitempty"`
	ExitCode int32  `json:"exit_code"`
}

// RunOneshot assembles the profile's context, resolves the LLM (override → the
// profile's declared llm → primary role), and runs the backend once in ONESHOT
// mode with stdout captured. It mirrors memory/compactor's distillation run: the
// client factory abstracts backend construction, the model rides in RunOptions,
// and SkipSetup keeps startup minimal (no hooks/MCP/statusline) — the profile's
// assembled context is the only specialization.
func RunOneshot(ctx context.Context, cfg *config.Config, req RunOneshotRequest) (*RunOneshotResult, error) {
	ctxResult, err := AssembleContext(ctx, cfg, AssembleContextRequest{
		Profile: req.Profile,
		Loader:  req.Loader,
	})
	if err != nil {
		return nil, fmt.Errorf("assemble context: %w", err)
	}

	label := resolveOneshotLabel(cfg, req.LLM, ctxResult.ProfileLLM)
	backendName, model := ResolveBackend(cfg, label)

	// The single-profile oneshot's axes: the session-level workspace default
	// (cfg.Workspace) x the project runtime default (cfg.Runtime — a bare
	// profile has no agent binding to declare one). Build the shared
	// executable trust gate ONLY when some isolation is actually requested (an
	// all-defaults oneshot writes no per-member config and must stay
	// byte-identical to pre-P3; gate construction runs the trust baseline +
	// opens the store). Ignored on the injected-Factory path.
	axes := isolation.Axes{Workspace: isolation.WorkspaceAxis(cfg.Workspace), Runtime: isolation.RuntimeAxis(cfg.Runtime)}
	var gate bundles.ContentGate
	if !axes.Zero() {
		gate = NewExecutableTrustGate(cfg).Gate()
	}
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
// single profile) and the map/weave fan (which resolves an agent or a
// bare-profile member), so the backend-launch tail is written once.
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
	// container), it scopes the per-member ManagedConfig (mcp/hooks/skills) written
	// natively into the isolated cwd, mirroring the top-level run. Ignored for a
	// none member (which shares the project cwd and writes no managed config).
	Profiles []string
	// Gate is the shared executable trust gate (built ONCE per fan) threaded into
	// the isolated member's ManagedConfig assembly, so bundle MCP/hooks/skill
	// exports gate at their own choke exactly as the top-level run. nil = no gating
	// (and none members never consult it).
	Gate bundles.ContentGate

	Factory pb.ClientFactory // nil self-invokes the compiled-in backend
}

// IsolationImageConfig assembles the user's container-image configuration for
// a backend's isolated runs: the per-backend prebuilt-image override (config
// isolation_images) plus the base Containerfile local builds layer the agent
// stage onto (config isolation_base_containerfile).
func IsolationImageConfig(cfg *config.Config, backend string) isolation.ImageConfig {
	return isolation.ImageConfig{
		Image:             cfg.IsolationImageFor(backend),
		BaseContainerfile: cfg.IsolationBaseContainerfilePath(),
	}
}

// runResolvedAgent launches the resolved backend once in ONESHOT mode with the
// composed context as the lead fragment and stdout captured. It carries no
// context-assembly or LLM-resolution logic — those happen upstream (RunOneshot
// for a single profile, ResolveAgent for an agent/bare-profile member) — so
// the two paths share one backend-launch tail and can never drift.
func runResolvedAgent(ctx context.Context, req resolvedRunRequest) (*RunOneshotResult, error) {
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
	if factory == nil {
		// Member isolation. Prepare realizes the per-axis degrade chain: a
		// runtime-axis failure drops only the container dimension (a requested
		// worktree survives); a workspace-axis failure degrades worktree→none.
		// It warns at each degrade and never blocks — None never fails. The
		// none tier loses cwd config isolation (shared project dir), the
		// documented non-git edge.
		policy, ws := isolation.Prepare(ctx, req.Axes, req.Backend, req.IsolationImage, req.WorkDir, req.AgentID)
		workDir = ws.Dir()
		// Per-agent config-home envs (worktree) isolate each engine's GLOBAL
		// config layer; nil for none/container. Threaded into the member's engine
		// env below so the shared ~/.claude.json etc. don't clobber.
		workspaceEnv = isolation.WorkspaceEnv(ws)
		// Tear the workspace down after the run. Registered BEFORE the client so
		// it runs AFTER client.Kill() (kill the plugin before removing its
		// workspace — WIP-safe for the worktree teardown). none's cleanup is a noop.
		defer func() { _ = ws.Cleanup() }()
		factory = isolation.FactoryForWorkspace(policy, ws)

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

	runReq := &pb.RunStart{
		Fragments: fragments,
		Prompt:    &pb.Fragment{Content: req.Task},
		Options: &pb.RunOptions{
			WorkDir: workDir,
			Env:     workspaceEnv,
			// Fan-out is ALWAYS non-interactive ONESHOT: there is no human to answer
			// the engine's permission prompt, so a member must bypass or it hangs.
			// Interactive runs resolve the posture from config/CLI; here it is the
			// invariant bypass.
			PermissionMode: agent.PermissionBypass.String(),
			Mode:           pb.ExecutionMode_ONESHOT,
			Model:          req.Model,
			Verbosity:      uint32(req.Verbosity * 16),
			SkipSetup:      skipSetup,
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
	if exitCode != 0 {
		return nil, fmt.Errorf("agent exited with code %d: %s", exitCode, strings.TrimSpace(stderr.String()))
	}

	return &RunOneshotResult{
		Output:   strings.TrimSpace(stdout.String()),
		Label:    req.Label,
		Backend:  req.Backend,
		Model:    req.Model,
		ExitCode: exitCode,
	}, nil
}

// ResolveBackend maps a config label to its backend type and model. A label that
// is not a configured entry but names a registered backend type resolves to that
// backend directly (the ad-hoc `--llm <type>` convenience); otherwise
// cfg.ResolveLLM's lookup/default applies. Shared by `run` and the
// oneshot/map/weave path so backend resolution is identical everywhere.
func ResolveBackend(cfg *config.Config, label string) (backend, model string) {
	backend, model = cfg.ResolveLLM(label)
	if _, configured := cfg.LM.Configs[label]; !configured && backends.Exists(label) {
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
