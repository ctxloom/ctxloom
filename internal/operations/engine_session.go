// ISO0 (v0.7.0 ACP Hub plan): OpenEngineSession is the frontend-neutral
// session opener extracted from `ctxloom acp`'s former openACPEngineChat. It
// does the load-bearing work that gives ctxloom its value — reads config
// from the session cwd, assembles context, mints the harp, applies the MCP
// trust gate, and stands up the engine conversation — so that value
// injection (context assembly, trust gate, harp mint) lives in ONE place no
// frontend can forget to call. Only `internal/cli`'s `ctxloom acp` command
// calls it today; ISO1 (container runtime) and ISO2 (worktree workspace) are
// expected to reuse it for other ACP-shaped session surfaces.
package operations

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// EngineSessionCoordinator abstracts the ACP-hosted runtime coordinator's
// reach-back services that OpenEngineSession needs: minting a session's
// delegation-credential env, and building its child-update watch closure.
// It is declared here — rather than OpenEngineSession taking a
// *agentcoord/coord.Coordinator directly — because that package already
// imports operations (children.go, spawner.go); a direct import back would
// cycle. `internal/cli`'s acpCoordinator (coord_acp.go, acp_children.go)
// satisfies this interface structurally, with no import of this package's
// interface type required on its side.
type EngineSessionCoordinator interface {
	// SessionEnv returns the coordinator reach-back trio for one session's
	// engine spawn env (nil on any standup/mint failure — the caller degrades
	// to no delegation reach-back).
	SessionEnv(cfg *config.Config, cwd, harp string) map[string]string
	// WatchChildren returns the EngineChat.WatchChildren closure for a
	// session hosted by this coordinator, or nil when no coordinator has
	// stood up yet (delegation degraded — the session behaves as if no
	// coordinator existed).
	WatchChildren() func(ctx context.Context) (<-chan ChildUpdate, func())
}

// openEngineSessionGateMu serializes each session-open's strictness window
// (Checkpoint → config load/assembly → findingsError). Sessions open on
// concurrent goroutines, but strictness Marks only isolate SEQUENTIAL windows
// (see strictness.Mark): unserialized opens interleave, and one session's
// fatal finding would land inside — and refuse — every concurrently-opening
// session. The lock covers assembly only, never the engine launch below it,
// so a slow engine spawn does not block other opens.
var openEngineSessionGateMu sync.Mutex

// newACPEngineClient is OpenEngineSession's seam onto
// pb.NewSelfInvokingClientForLabelEnv — a package var, mirroring oneshot.go's
// prepareIsolation seam style, so a test can drive the WHOLE opener
// (including the ISO2 workspace-axis wiring immediately above this call) with
// a fake pb.Client instead of spawning a real `ctxloom llm serve <backend>`
// subprocess (which a unit test cannot do: it would try to self-exec the
// TEST binary as the plugin).
var newACPEngineClient = func(backendName, label string, verbosity int, spawnEnv map[string]string) (pb.Client, error) {
	return pb.NewSelfInvokingClientForLabelEnv(backendName, label, verbosity, spawnEnv)
}

// OpenEngineSession is the production ChatOpener body: it loads ctxloom
// config for the session's cwd, assembles the context (an agent's composed
// profiles, or the profile flow), resolves the engine label (override →
// agent engine / profile llm → primary), records the session under a harp,
// and opens the plugin's structured chat — the same substrate `run
// --structured` drives. For a resume (session/load) it additionally fetches
// the recorded harp's history: the entries replay to the ACP client, and a
// rendered transcript primes the fresh engine via the first-turn lead block.
//
// ISO2 (WORKSPACE axis): flagWorkspace is the session-level --workspace
// override (isolation.WorkspaceAxis values "none"|"worktree", mirroring
// `ctxloom run`'s flag), honored ONLY for a session bound to an EXPLICIT
// flagAgent — never the plain `ctxloom acp` entry — see prepareACPWorkspace's
// doc for why and how that gate is drawn.
func OpenEngineSession(ctx context.Context, req OpenRequest, acpCoord EngineSessionCoordinator, flagProfile, flagAgent, llmOverride, flagWorkspace string) (*EngineChat, error) {
	var (
		cfg             *config.Config
		profile         string
		contextText     string
		label           string
		defaultProfiles []string
		sessionProfiles []string
		currentAgent    string
		backendName     string
		model           string
		mcpServers      []agent.ChatMCPServer
	)
	// Fail-loudly gate, per session: checkpoint before config load + assembly
	// so this session's fatal findings don't bleed across sessions (the process
	// keeps running). In strict mode the findingsError closing the window
	// surfaces them to the editor as a session-open failure; in degraded mode
	// it is a no-op and the session opens with whatever context survived. The
	// whole window is serialized (openEngineSessionGateMu) because concurrent
	// opens would interleave their Mark windows — see the var's doc.
	if gerr := func() error {
		openEngineSessionGateMu.Lock()
		defer openEngineSessionGateMu.Unlock()
		startupMark := strictness.Checkpoint()

		var err error
		cfg, err = loadConfigForDir(req.Cwd)
		if err != nil {
			return err
		}

		profile = req.Profile
		if profile == "" {
			profile = flagProfile
		}

		// An agent binding resolves engine + composed profiles in one step. Per
		// fault tolerance an unresolvable agent degrades to the plain profile
		// flow (warn) rather than refusing the editor's session. Absent an explicit
		// --agent, bind the always-bound DEFAULT AGENT (cfg.DefaultAgent) — the same
		// bare-launch binding `ctxloom run` applies (profiles.defaults was retired);
		// an empty/unresolvable default_agent simply degrades to the profile flow.
		resolveAgent := flagAgent
		if resolveAgent == "" {
			resolveAgent = cfg.DefaultAgent
		}
		if resolveAgent != "" {
			if rs, rerr := ResolveAgent(ctx, cfg, resolveAgent, llmOverride); rerr != nil {
				clidiag.Warn("ctxloom", "acp agent: agent %q unavailable; opening a default session: %v", resolveAgent, rerr)
			} else {
				contextText = rs.Context
				label = rs.Label
				currentAgent = resolveAgent
				sessionProfiles = rs.Profiles
				if rs.Runtime != "" && rs.Runtime != string(isolation.RuntimeHost) {
					// ACP sessions run in-process at the editor's cwd — a
					// containerized engine the editor cannot reach would be worse
					// than none. The session paths (run/map/weave) are where the
					// runtime axis applies.
					clidiag.Warn("ctxloom", "acp agent: agent %q declares runtime %q; ACP sessions run at the editor's cwd, so the runtime axis is ignored here", resolveAgent, rs.Runtime)
				}
			}
		}

		if currentAgent == "" {
			// Context assembly is fault-tolerant (CLAUDE.md): a failed assembly
			// warns and the session still opens — the engine runs without ctxloom
			// context rather than refusing the editor's session.
			var profileLLM string
			if ctxResult, cerr := AssembleContext(ctx, cfg, AssembleContextRequest{Profile: profile}); cerr != nil {
				clidiag.Warn("ctxloom", "acp agent: context assembly failed; continuing without context: %v", cerr)
			} else {
				contextText = ctxResult.Context
				profileLLM = ctxResult.ProfileLLM
				defaultProfiles = ctxResult.Profiles
				sessionProfiles = ctxResult.Profiles
			}

			label = llmOverride
			if label == "" {
				label = profileLLM
			}
			if label == "" {
				label = cfg.PrimaryLabel()
			}
		}
		backendName, model = ResolveBackend(cfg, label)

		// An ACP session never runs backend Setup (which writes the managed MCP
		// servers into the engine's settings file), so the managed set rides
		// session/new mcpServers instead. Bundle executables pass the SAME trust
		// gate the run path applies before reaching the engine (fail-closed);
		// client-supplied servers win by name over the managed entries.
		execGate := NewExecutableTrustGate(cfg)
		cfg.SetExecutableTrustGate(execGate.Gate())
		mcpServers = append(append([]agent.ChatMCPServer{}, req.MCPServers...),
			acpSessionMCPServers(cfg, backendName, sessionProfiles, req.MCPServers)...)
		execGate.WarnWithheld()

		// Strict mode: refuse to open the session when config load or assembly
		// recorded a fatal finding, surfacing the full list to the editor. Degraded
		// mode returns nil here and the session opens (ACP's usual fault tolerance).
		return engineSessionFindingsError(startupMark)
	}(); gerr != nil {
		return nil, gerr
	}

	// ISO2: resolve the WORKSPACE axis. See prepareACPWorkspace's doc for the
	// full gate (only an EXPLICIT --agent binding may isolate; the plain
	// `ctxloom acp` entry never does, regardless of cfg.Workspace or an
	// auto-bound cfg.DefaultAgent). The RUNTIME axis deliberately stays the
	// zero value (host) here — ISO1 owns that axis on this same opener.
	acpAxes := isolation.Axes{Workspace: acpWorkspaceAxis(cfg, flagAgent, currentAgent, flagWorkspace)}

	// Session accounting: resume the named harp, or mint a fresh one. A resume
	// of an unknown/unbound harp is a hard error (the client asked for THAT
	// session); a failed mint merely degrades — the session runs unrecorded.
	harp := req.ResumeHarp
	var replay []agent.SessionEntry
	if harp != "" {
		entries, rerr := RecordedSessionEntries(ctx, harp)
		if rerr != nil {
			return nil, rerr
		}
		replay = entries
		contextText = JoinLeadBlocks(contextText, RenderResumedTranscript(harp, replay))
	} else if entry, aerr := AssignSession(req.Cwd, backendName); aerr != nil {
		clidiag.Warn("ctxloom", "acp agent: session accounting unavailable; session will not be resumable: %v", aerr)
	} else {
		harp = entry.HarpName
	}

	env := map[string]string{}
	var spawnEnv map[string]string
	if harp != "" {
		// The engine (and its SessionStart hooks) see the harp exactly like an
		// interactive run — this is what binds the engine's transcript to the
		// harp so the session becomes loadable later.
		env["CTXLOOM_SESSION_HARP"] = harp
		// Coordinator reach-back trio: stamped onto the RUNNER's spawn env
		// (B1.6 — the runner terminates MCP and holds the one credential;
		// the engine env never carries it). ACP sessions run host-runtime at
		// the editor's cwd, so the loopback endpoint reaches the runner.
		if trio := acpCoord.SessionEnv(cfg, req.Cwd, harp); len(trio) > 0 {
			spawnEnv = trio
			spawnEnv["CTXLOOM_SESSION_HARP"] = harp
		}
	}

	// ISO2: prepare the resolved workspace (a no-op nil for the default host
	// cwd — every session without an isolating agent binding). aw.cleanup MUST
	// run on every path out of here from this point on (the two error returns
	// below, and closeOnce further down) — see prepareACPWorkspace's doc.
	aw, werr := prepareACPWorkspace(ctx, cfg, acpAxes, backendName, currentAgent, req.Cwd, env)
	if werr != nil {
		return nil, werr
	}
	workDir := req.Cwd
	if aw != nil {
		workDir = aw.dir
		if len(aw.env) > 0 {
			merged := make(map[string]string, len(aw.env)+len(env))
			maps.Copy(merged, aw.env)
			maps.Copy(merged, env) // an already-set var (CTXLOOM_SESSION_HARP) wins
			env = merged
		}
	}

	client, err := newACPEngineClient(backendName, label, 0, spawnEnv)
	if err != nil {
		if aw != nil {
			aw.cleanup()
		}
		return nil, err
	}
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir: workDir,
		Model:   model,
		Env:     env,
		// The connected editor answers session/request_permission — real
		// interactive approvals in structured mode (no terminal prompt here).
		ForwardPermissions: true,
		MCPServers:         mcpServers,
	})
	if err != nil {
		client.Kill()
		if aw != nil {
			aw.cleanup()
		}
		return nil, err
	}
	if aw != nil && aw.announce != "" {
		// D-ISO (announce-only, USER-DECIDED): the editor is otherwise BLIND to
		// the isolated worktree — no ACP method lets an agent make a client open
		// a folder. This is the mechanism available today: a synthetic system
		// entry riding as the FIRST event of the FIRST turn, ahead of the
		// engine's own output, rendering as a visible agent_message_chunk in
		// the client (mapping.go's EntryTypeSystem case — no wire/mapping
		// changes needed here, only the event this opener hands the server).
		events = announceOnFirstEvent(ctx, events, aw.announce)
	}

	closeOnce := sync.OnceFunc(func() {
		client.Kill()
		// ISO2 lifecycle: kill the engine BEFORE removing its workspace (the
		// worktree teardown is WIP-safe and git-based; nothing should still be
		// running against the checkout while it runs). A nil aw (the default,
		// unisolated path) makes this a no-op.
		if aw != nil {
			aw.cleanup()
		}
		if harp != "" {
			if merr := MarkSessionEnded(harp, time.Now()); merr != nil {
				clidiag.Warn("ctxloom", "acp agent: mark session ended: %v", merr)
			}
		}
	})
	// D3 (manly-grant (2)): a session gets the child-update push only when
	// the coordinator actually stood up (nil on a bare harp-less session, or
	// a degraded standup that already warned) — see EngineSessionCoordinator.
	watchChildren := acpCoord.WatchChildren()

	return &EngineChat{
		Context:       contextText,
		In:            in,
		Events:        events,
		Errs:          errs,
		Close:         closeOnce,
		Harp:          harp,
		Modes:         buildSessionModes(cfg, profile, defaultProfiles, currentAgent),
		AssembleMode:  assembleModeFunc(cfg, label),
		Replay:        replay,
		LLMs:          buildSessionLLMs(cfg, label),
		WatchChildren: watchChildren,
	}, nil
}

// engineSessionFindingsError renders the strictness findings since mark as an
// error refusing the session open. This mirrors internal/cli's findingsError
// (startup_helpers.go) and agentcoord/coord/spawner.go's own copy — each
// layer keeps its own because none of the three may import another here
// (operations cannot import coord, coord cannot import cli, and this is the
// per-call/keeps-running variant, not the process-exit variant cli uses at
// its own top-level startup).
func engineSessionFindingsError(mark strictness.Mark) error {
	found := strictness.Since(mark)
	if len(found) == 0 || strictness.Degraded() {
		return nil
	}
	msg := "fatal startup findings:"
	for _, f := range found {
		msg += "\n  - " + f.Message
		if f.FixIt != "" {
			msg += " (fix: " + f.FixIt + ")"
		}
	}
	return errors.New(msg)
}

// acpSessionMCPServers composes the ctxloom-managed MCP injection for one ACP
// session from the session-cwd config: the same sources Setup's settings write
// reconciles — ctxloom's auto-registered context server, builtin-bundle
// companions (taskloom), and the config/profile servers scoped to the
// session's profile set — minus any name the client already supplied
// (existing wins). The cfg-holding seams are used (not AssembleManagedConfig,
// which re-loads config from the process cwd) so the session's own config and
// its attached trust gate are honored.
func acpSessionMCPServers(cfg *config.Config, backendName string, profiles []string, existing []agent.ChatMCPServer) []agent.ChatMCPServer {
	return agent.ComposeChatMCPServers(backendName,
		backends.AssembleManagedMCP(cfg, profiles),
		cfg.ResolveBundleMCPServers(profiles),
		existing)
}

// buildSessionLLMs advertises the cwd's configured LLMs (the labels `-l`/`--llm`
// accepts, matching `ctxloom llm list`) so a client can DISPLAY the available
// engines and mark the launched one (current). This is advertisement only: the
// session's engine is pinned at launch — a live mid-session LLM switch is not
// implemented. nil when no LLMs are enumerable.
func buildSessionLLMs(cfg *config.Config, current string) *SessionLLMs {
	names := AvailableLLMNames(cfg)
	if len(names) == 0 {
		return nil
	}
	llms := &SessionLLMs{Current: current}
	for _, n := range names {
		llms.Available = append(llms.Available, LLMInfo{ID: n, Name: n})
	}
	return llms
}

// agentModePrefix namespaces agent mode IDs so an agent can never
// collide with a profile of the same name.
const agentModePrefix = "agent:"

// agentModeID is the ACP mode id advertising the named agent.
func agentModeID(name string) string { return agentModePrefix + name }

// buildSessionModes surfaces the cwd's ctxloom profile sets as ACP session
// modes: a synthetic "default" mode for the configured default set (which may
// compose several profiles), one mode per installed profile, and one mode per
// agent (its composed profile set; the engine binding applies only at
// launch — see assembleModeFunc). nil when the profile list is unavailable —
// the session simply advertises no modes.
func buildSessionModes(cfg *config.Config, initialProfile string, defaultProfiles []string, currentAgent string) *SessionModes {
	loader := cfg.GetProfileLoader()
	if loader == nil {
		return nil
	}
	list, err := loader.List()
	if err != nil {
		clidiag.Warn("ctxloom", "acp agent: listing profiles for session modes: %v", err)
		return nil
	}
	profileNames := make([]string, 0, len(list))
	for _, p := range list {
		profileNames = append(profileNames, p.Name)
	}
	return sessionModesFrom(profileNames, ListAgents(cfg), initialProfile, defaultProfiles, currentAgent)
}

// sessionModesFrom is the pure mode-list builder behind buildSessionModes:
// default set, profiles, then agents, with the current mode following the
// launch selection (agent > profile > default).
func sessionModesFrom(profileNames []string, subs []AgentEntry, initialProfile string, defaultProfiles []string, currentAgent string) *SessionModes {
	if len(profileNames) == 0 && len(subs) == 0 {
		return nil
	}

	defaultName := "default"
	if len(defaultProfiles) > 0 {
		defaultName = "default (" + strings.Join(defaultProfiles, ", ") + ")"
	}
	modes := &SessionModes{
		Current:   DefaultModeID,
		Available: []SessionMode{{ID: DefaultModeID, Name: defaultName}},
	}
	seen := false
	for _, name := range profileNames {
		modes.Available = append(modes.Available, SessionMode{ID: name, Name: name, Profiles: []string{name}})
		seen = seen || name == initialProfile
	}
	for _, s := range subs {
		modes.Available = append(modes.Available, SessionMode{
			ID:       agentModeID(s.Name),
			Name:     s.Name + " (agent)",
			Profiles: s.Profiles,
			Engine:   s.Engine,
		})
	}
	switch {
	case currentAgent != "":
		modes.Current = agentModeID(currentAgent)
	case initialProfile != "":
		if !seen {
			modes.Available = append(modes.Available, SessionMode{ID: initialProfile, Name: initialProfile, Profiles: []string{initialProfile}})
		}
		modes.Current = initialProfile
	}
	return modes
}

// assembleModeFunc backs session/set_mode: re-assemble the lead context for
// the chosen mode's profile set (nil = the configured defaults). The session's
// ENGINE is pinned at launch: an agent mode declaring a different engine
// still re-composes that agent's context, and the warning names the launch
// flag that honors the binding fully.
func assembleModeFunc(cfg *config.Config, sessionLabel string) func(ctx context.Context, mode SessionMode) (string, error) {
	return func(ctx context.Context, mode SessionMode) (string, error) {
		if mode.Engine != "" && mode.Engine != sessionLabel {
			clidiag.Warn("ctxloom", "acp agent: mode %q declares engine %q but this session runs %q — the engine is pinned at launch (use `ctxloom acp --agent %s` to honor it)",
				mode.ID, mode.Engine, sessionLabel, strings.TrimPrefix(mode.ID, agentModePrefix))
		}
		res, err := AssembleContext(ctx, cfg, AssembleContextRequest{Profiles: mode.Profiles})
		if err != nil {
			return "", err
		}
		return res.Context, nil
	}
}

// loadConfigForDir loads ctxloom config as seen FROM dir (the ACP session's
// cwd, which need not be this server process's cwd): it walks dir upward for a
// .ctxloom directory and pins config loading to it; absent one, the default
// discovery (process cwd, then home) applies.
func loadConfigForDir(dir string) (*config.Config, error) {
	for d := dir; ; {
		appDir := filepath.Join(d, config.AppDirName)
		if info, err := os.Stat(appDir); err == nil && info.IsDir() {
			return config.Load(config.WithAppDir(appDir))
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return config.Load()
}

// ISO2 (v0.7.0 ACP Hub plan): the WORKSPACE isolation axis for ACP-hosted
// sessions. `internal/lm/isolation` already draws the axis as an
// ORCHESTRATION trait the invocation supplies (isolation.go's doc: needing a
// private cwd is a property of how you fan, not of who the agent is), and
// `ctxloom run --agent`/agent_run (internal/cli/run.go, delegate.go) already
// resolve+prepare it via isolation.Prepare/isolation.WorkspaceEnv over the
// oneshot.go package-level seams (prepareIsolation, isolationGateMu,
// isolationGateErr). Nothing isolation-specific is reinvented below — this is
// that same machinery wired onto the ACP opener, plus one ACP-only posture
// rule layered on top (see acpWorkspaceAxis).

// acpWorkspaceAxis decides the workspace-axis VALUE for one ACP session:
// flagWorkspace (this invocation's --workspace) else the project's
// `workspace:` default — but ONLY when the session is bound to an EXPLICIT
// --agent, never the plain `ctxloom acp` entry (D-ISO's posture: worktree-
// under-ACP is for deliberately-isolated agent bindings — reviewer/executor
// agents an editor configures as their OWN client entry via `ctxloom acp
// --agent <name>`, see 'ctxloom acp agents' — never a silent default for the
// entry with no --agent).
//
// currentAgent != "" is not sufficient on its own to detect "an explicit
// --agent was given": an unset --agent still auto-binds the project's
// cfg.DefaultAgent (see OpenEngineSession's resolveAgent fallback above), so
// a project that merely SETS default_agent would otherwise get its plain
// entry silently isolated too. resolveAgent is assigned flagAgent verbatim,
// unconditionally, whenever flagAgent != "" — so currentAgent == flagAgent
// holds exactly when the explicit-agent path resolved successfully; that
// equality (not just currentAgent != "") is the honest test for "an editor
// deliberately chose this agent's own entry".
func acpWorkspaceAxis(cfg *config.Config, flagAgent, currentAgent, flagWorkspace string) isolation.WorkspaceAxis {
	if flagAgent == "" || currentAgent != flagAgent {
		return isolation.WorkspaceAxis("")
	}
	ws := flagWorkspace
	if ws == "" {
		ws = cfg.Workspace
	}
	return isolation.WorkspaceAxis(ws)
}

// acpWorkspace is ISO2's prepared workspace-axis state for one ACP session:
// where the engine's cwd lives, the per-agent config-home env additions
// (worktree only — CLAUDE_CONFIG_DIR/CODEX_HOME/KIRO_HOME isolating the
// engine's GLOBAL layer), the first-turn announcement text (worktree only),
// and its teardown. A nil *acpWorkspace (prepareACPWorkspace's fast path)
// means the default host cwd applies — the overwhelming common case,
// including every session without an isolating --agent binding.
type acpWorkspace struct {
	dir      string
	env      map[string]string
	announce string
	cleanup  func()
}

// prepareACPWorkspace resolves and prepares ISO2's workspace axis for one ACP
// session. It returns (nil, nil) — no isolation.Prepare call at all — when
// axes asks for nothing but the shared project dir (acpWorkspaceAxis already
// enforces the "no --agent → never worktree" posture, so this is the path
// for the plain `ctxloom acp` entry and any --agent session that didn't ask
// for a worktree).
//
// Otherwise this is the SAME checkpoint→Prepare→gate window
// oneshot.go's runResolvedAgent and delegate.go's PrepareAgentChat run over
// the package's shared prepareIsolation/isolationGateMu/isolationGateErr
// seams: a ClassIsolation finding (e.g. grave-prize's no-host-credentials-to-
// seed case, or antigravity's no-config-home-lever case) refuses the session
// open in strict mode exactly like a fan member would refuse itself — rather
// than silently launching an unseeded/logged-out engine — and the
// already-prepared (but refused) workspace is torn down before returning the
// error. The RUNTIME axis is left at its zero value (isolation.Axes.Runtime
// defaults to host) — ISO1 owns that axis on this same opener.
func prepareACPWorkspace(ctx context.Context, cfg *config.Config, axes isolation.Axes, backendName, agentID, projectDir string, env map[string]string) (*acpWorkspace, error) {
	if !axes.WantsWorktree() {
		return nil, nil
	}
	isolationGateMu.Lock()
	mark := strictness.Checkpoint()
	policy, ws := prepareIsolation(ctx, axes, backendName, IsolationImageConfig(cfg, backendName), projectDir, agentID, isolation.SessionStateFromEnv(env))
	found := strictness.Since(mark)
	isolationGateMu.Unlock()

	cleanup := func() { _ = ws.Cleanup() }
	if gerr := isolationGateErr(found); gerr != nil {
		cleanup()
		return nil, gerr
	}
	aw := &acpWorkspace{dir: ws.Dir(), env: isolation.WorkspaceEnv(ws), cleanup: cleanup}
	if isolation.Isolated(policy) {
		// isolation.Isolated is false when the chain degraded worktree→none
		// (not a git repo, or `git worktree add` failed) — a silent,
		// benign fallback (isolation.go's doc), so no announcement fires and
		// aw.dir/aw.env are the harmless projectDir/nil the None tier
		// produces. Only a REAL worktree gets the honesty announcement.
		aw.announce = fmt.Sprintf(
			"ctxloom: this agent's session is isolated to its own git worktree — %s. "+
				"Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, and this window stays blind to it unless you open the path yourself. Results return through ctxloom's normal delegated-child assemble/merge flow.",
			aw.dir)
	}
	return aw, nil
}

// announceOnFirstEvent wraps an engine's chat events so the very FIRST event
// delivered is a synthetic system entry carrying msg — rendering as a visible
// agent_message_chunk in the ACP client (acpagent/mapping.go's
// EntryTypeSystem case) ahead of anything the engine itself says on the
// session's first turn. This is the ANNOUNCE-ONLY mechanism D-ISO settled on:
// no ACP method lets an agent make a client open a folder, so a visible
// message on the first turn is what's available today (a dedicated
// session-info surface is a later slice). ctx bounds the forwarding
// goroutine to the session's own lifetime, so a session closed before any
// prompt arrives never leaks it.
func announceOnFirstEvent(ctx context.Context, events <-chan agent.ChatEvent, msg string) <-chan agent.ChatEvent {
	out := make(chan agent.ChatEvent)
	go func() {
		defer close(out)
		select {
		case out <- agent.ChatEvent{Entry: &agent.SessionEntry{Type: agent.EntryTypeSystem, Content: msg}}:
		case <-ctx.Done():
			return
		}
		for {
			select {
			case ev, ok := <-events:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}
