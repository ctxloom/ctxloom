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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
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

// noEngineSessionCoordinator is the nil coordinator: a frontend that hosts no
// runtime coordinator at all passes nil, and OpenEngineSession substitutes
// this. Its answers are exactly the ones the interface already documents for
// "no coordinator stood up" — no reach-back trio, no child-update watch — so
// the degraded path is the SAME one a live-but-unstood-up coordinator takes,
// rather than a second one reached by a nil check at each call.
type noEngineSessionCoordinator struct{}

func (noEngineSessionCoordinator) SessionEnv(*config.Config, string, string) map[string]string {
	return nil
}

func (noEngineSessionCoordinator) WatchChildren() func(ctx context.Context) (<-chan ChildUpdate, func()) {
	return nil
}

// assignSession is OpenEngineSession's seam onto AssignSession (a package
// var, like newACPEngineClient below): minting a harp touches the real
// session store, and the DEGRADE this opener takes when it fails — three
// facilities silently off for the rest of the session — is otherwise
// undrivable from a test.
var assignSession = AssignSession

// newACPEngineClient is OpenEngineSession's seam onto
// pb.NewSelfInvokingClientForLabelEnv — a package var, mirroring oneshot.go's
// prepareIsolation seam style, so a test can drive the WHOLE opener
// (including the ISO2 workspace-axis wiring immediately above this call) with
// a fake pb.Client instead of spawning a real `ctxloom llm serve <backend>`
// subprocess (which a unit test cannot do: it would try to self-exec the
// TEST binary as the plugin).
var newACPEngineClient = func(backendName, label string, verbosity int, spawnEnv map[string]string) (pb.Client, error) {
	// Return an explicit nil interface on failure. Forwarding the concrete
	// (*LLMRunner, error) pair directly would box a typed nil into a non-nil
	// pb.Client, and every `if client != nil` check downstream tests the
	// interface, not the pointer.
	runner, err := pb.NewSelfInvokingClientForLabelEnv(backendName, label, verbosity, spawnEnv)
	if err != nil {
		return nil, err
	}
	return runner, nil
}

// OpenEngineSession is the production ChatOpener body: it loads ctxloom
// config for the session's cwd, assembles the context (an agent's composed
// profiles, or the profile flow), resolves the engine label (override →
// agent engine / profile llm → primary), records the session under a harp,
// and opens the plugin's structured chat — the same substrate `ctxloom acp
// run`'s session form drives. For a resume (session/load) it additionally fetches
// the recorded harp's history: the entries replay to the ACP client, and a
// rendered transcript primes the fresh engine via the first-turn lead block.
//
// acpCoord may be nil: a frontend that hosts no runtime coordinator gets the
// same degraded behaviour as one whose coordinator never stood up (no
// delegation reach-back, no child-update push) rather than a panic.
//
// ISO2 (WORKSPACE axis): flagWorkspace is the session-level --workspace
// override (isolation.WorkspaceAxis values "none"|"worktree", mirroring
// `ctxloom run`'s flag), honored ONLY for a session bound to an EXPLICIT
// flagAgent — never the plain `ctxloom acp` entry — see prepareACPWorkspace's
// doc for why and how that gate is drawn.
func OpenEngineSession(ctx context.Context, req OpenRequest, acpCoord EngineSessionCoordinator, flagProfile, flagAgent, llmOverride, flagWorkspace string) (*EngineChat, error) {
	if acpCoord == nil {
		acpCoord = noEngineSessionCoordinator{}
	}
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
		runtimeAxis     agent.RuntimeAxis
		// fragmentsLoaded names what actually loaded into contextText — set
		// from ResolvedAgent.Fragments (agent path) or
		// AssembleContextResult.FragmentsLoaded (bare profile-flow path) below.
		// Carried through to buildSessionInitSummary (ISO4), which renders it
		// via namesOrCount (names when short, a count + CLI pointer when long).
		fragmentsLoaded []string
		// assemblyFailed is non-nil when context assembly FAILED, as opposed
		// to succeeding with nothing to load. Without it the init summary
		// renders a failed assembly as the authoritative "fragments : none" —
		// the one artifact whose job is to say what ctxloom
		// assembled, claiming it assembled nothing when it does not know.
		assemblyFailed error
		// requestedAgent is the RAW name resolution was attempted against
		// (flagAgent, or the project's cfg.DefaultAgent when flagAgent was
		// empty) — hoisted alongside currentAgent so the ISO3 announcement
		// built after this closure returns can tell "no agent was ever asked
		// for" (requestedAgent == "") apart from "one was asked for and
		// resolution FAILED" (requestedAgent != "" but currentAgent == "").
		requestedAgent string
	)
	// Fail-loudly gate, per session: checkpoint before config load + assembly
	// so this session's fatal findings don't bleed across sessions (the process
	// keeps running). In strict mode the findingsError closing the window
	// surfaces them to the editor as a session-open failure; in degraded mode
	// it is a no-op and the session opens with whatever context survived.
	// Sessions open on concurrent goroutines, but no external serialization is
	// needed: strictness gives each goroutine's window its own findings log,
	// so a concurrently-opening session's finding can no longer
	// land inside — and wrongly refuse — this one.
	if gerr := func() error {
		startupMark := strictness.Checkpoint()
		defer strictness.Close(startupMark)

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
			resolveAgent = cfg.GetDefaultAgent()
		}
		requestedAgent = resolveAgent
		if resolveAgent != "" {
			if rs, rerr := ResolveAgent(ctx, cfg, resolveAgent, llmOverride); rerr != nil {
				// An EXPLICIT --agent (flagAgent != "") that cannot be honored
				// is FATAL. See agentBindingError for why a
				// degrade here is worse than a hard break. An agent auto-bound
				// from cfg.DefaultAgent still degrades: the editor never asked
				// for it, so a project that merely SET default_agent must not
				// hard-break every plain session.
				if flagAgent != "" {
					return agentBindingError(cfg, resolveAgent, rerr)
				}
				clidiag.Warn("ctxloom", "acp agent: default agent %q unavailable; opening a default session: %v", resolveAgent, rerr)
			} else {
				contextText = rs.Context
				label = rs.Label
				currentAgent = resolveAgent
				sessionProfiles = rs.Profiles
				fragmentsLoaded = rs.Fragments
				// ISO1: the runtime axis now rides ChatRequest.Runtime into the
				// engine's StructuredChat call below — a container-bound agent
				// runs its engine subprocess inside a container (same-path
				// workspace mount, reach-back via the runner-terminated MCP
				// socket), never silently on the host. See internal/acp's
				// container transport (acp.go) for the honoring half; a backend
				// whose structured chat does not implement it fails loudly
				// there rather than falling back to the host.
				runtimeAxis = rs.Runtime
			}
		}

		if currentAgent == "" {
			// Context assembly is fault-tolerant (CLAUDE.md): a failed assembly
			// warns and the session still opens — the engine runs without ctxloom
			// context rather than refusing the editor's session.
			var profileLLM string
			if ctxResult, cerr := AssembleContext(ctx, cfg, AssembleContextRequest{Profile: profile}); cerr != nil {
				assemblyFailed = cerr
				clidiag.Warn("ctxloom", "acp agent: context assembly failed; continuing without context: %v", cerr)
			} else {
				contextText = ctxResult.Context
				profileLLM = ctxResult.ProfileLLM
				defaultProfiles = ctxResult.Profiles
				sessionProfiles = ctxResult.Profiles
				fragmentsLoaded = ctxResult.FragmentsLoaded
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
		//
		// B3 (gap G11, HTTP/SSE passthrough) CONCLUSION on trust: req.MCPServers
		// (the editor's session/new payload) is NEVER run through this gate,
		// for ANY transport — stdio, http, or sse alike — and that is
		// unchanged by B3, not a new hole it opens. This gate exists to vet
		// CONTENT ctxloom itself resolves from a bundle/remote by publisher
		// signature (trust.Ref + EffectiveTrust); an editor's session/new
		// mcpServers has no publisher, no signature, no bundle — it is the
		// human's own direct configuration of THEIR OWN editor, structurally
		// outside what this gate was built to evaluate (a URL is no more
		// "gateable" by a signer-trust model than a stdio command is: neither
		// carries a Ref). Extending to http/sse therefore does not lower the
		// bar — client-supplied servers were already outside this gate's
		// domain before B3. See mcpServersFromACP/mcpServersToACP for the
		// actual delivery-time decision (an engine's own advertised
		// capability), which is a DIFFERENT axis (can the engine take it) from
		// trust (should ctxloom forward it) — B3 does not invent a new trust
		// policy for either axis.
		execGate := NewExecutableTrustGate(cfg)
		cfg.SetExecutableTrustGate(execGate.Authorizer())
		mcpServers = append(append([]agent.ChatMCPServer{}, req.MCPServers...),
			acpSessionMCPServers(cfg, backendName, sessionProfiles, req.MCPServers)...)
		execGate.WarnWithheld()

		// Strict mode: refuse to open the session when config load or assembly
		// recorded a fatal finding, surfacing the full list to the editor. Degraded
		// mode returns nil here and the session opens (ACP's usual fault tolerance).
		return strictness.FindingsError(startupMark)
	}(); gerr != nil {
		return nil, gerr
	}

	// ISO2: resolve the WORKSPACE axis. See prepareACPWorkspace's doc for the
	// full gate (only an EXPLICIT --agent binding may isolate; the plain
	// `ctxloom acp` entry never does, regardless of cfg.Workspace or an
	// auto-bound cfg.DefaultAgent). The RUNTIME axis deliberately stays the
	// zero value (host) here — ISO1 owns that axis on this same opener.
	acpWorkspace, err := acpWorkspaceAxis(cfg, flagAgent, currentAgent, flagWorkspace)
	if err != nil {
		return nil, fmt.Errorf("acp agent: %w", err)
	}
	acpAxes := isolation.Axes{Workspace: acpWorkspace}

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
	} else if entry, aerr := assignSession(ctx, req.Cwd, backendName); aerr != nil {
		// The harp is the load-bearing identifier for THREE separate
		// facilities, and every one of them is off for the rest of this
		// session (see the `if harp != ""` guards below and the closeOnce
		// tail). Naming only the first left the other two to be discovered
		// as unexplained absences mid-session.
		clidiag.Warn("ctxloom", "acp agent: session accounting unavailable (%v) — this session is NOT recorded and cannot be resumed, the engine and its hooks run WITHOUT CTXLOOM_SESSION_HARP, and delegation reach-back is off (no agent_run from this session)", aerr)
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

	// B5 (gap G14): see shouldChainFsUpstream's doc for THE ONE RULE this
	// enforces — leaving env untouched on the two isolating axes is what
	// makes worktree/container sessions keep reading local disk, with no
	// separate per-axis branch of their own (session.go's handleFsRead/
	// handleFsWrite) to get wrong.
	if req.FsUpstreamAddr != "" && shouldChainFsUpstream(aw, runtimeAxis) {
		env[FsUpstreamEnvVar] = req.FsUpstreamAddr
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
		// ForwardTerminal (B1, gap G6) rides straight through from the
		// caller's OpenRequest — only true when acpagent.Server's own
		// connected editor advertised clientCapabilities.terminal at
		// initialize; see OpenRequest.ForwardTerminal's doc comment.
		ForwardTerminal: req.ForwardTerminal,
		MCPServers:      mcpServers,
		Runtime:         runtimeAxis,
	})
	if err != nil {
		client.Kill()
		if aw != nil {
			aw.cleanup()
		}
		return nil, err
	}
	// ISO3/ISO4 (runtime-axis honesty, on top of D-ISO): the editor is
	// structurally blind to BOTH isolation axes, the resolved model, the
	// composed profiles, and everything ctxloom assembled (fragments,
	// commands, skills, MCP servers) — it handed ctxloom a cwd and got a
	// session back with no visibility into any of it. D-ISO already covered
	// the WORKSPACE axis (aw.announce, worktree-only); ISO3 extended that to
	// always state the resolved isolation posture; ISO4 (this slice) widens
	// it into the full session initialization summary — see
	// buildSessionInitSummary's doc for what it reports and why.
	//
	// sessionCommands is built once and reused: both the summary text below
	// and EngineChat.Commands (the ACP available_commands_update surface)
	// need the same ListCommands result, and this avoids listing twice.
	sessionCommands, cmdErr := buildSessionCommands(ctx, cfg)
	commandNames := listed(nil)
	switch {
	case cmdErr != nil:
		commandNames = listingFailed(cmdErr)
	case sessionCommands != nil:
		names := make([]string, 0, len(sessionCommands.Available))
		for _, c := range sessionCommands.Available {
			names = append(names, c.Name)
		}
		commandNames = listed(names)
	}
	skillNames := listSessionSkillNames(ctx, cfg)
	mcpServerNames := listed(MCPServerNames(mcpServers))
	fragments := listed(fragmentsLoaded)
	if assemblyFailed != nil {
		fragments = listingFailed(assemblyFailed)
	}

	// AT-CONNECT (not per-turn): this used to ride the Events channel as a
	// synthetic first entry (announceOnFirstEvent, since deleted) — which
	// only ever reached the client once a session/prompt actually ran a turn
	// (acpagent's runTurn is the only Events reader), so a connected editor
	// that hadn't sent its first prompt yet saw nothing. The text is plain
	// session data instead: EngineChat.InitSummary, delivered by whatever
	// frontend hosts this opener as soon as the session itself exists — for
	// ACP that is a session/update notification emitted right after
	// session/new|load, before the editor ever gets to send session/prompt
	// (see acpagent's emitSessionInitSummary, announce.go).
	initSummary := buildSessionInitSummary(sessionInitSummaryInputs{
		cfg:             cfg,
		backendName:     backendName,
		requestedAgent:  requestedAgent,
		currentAgent:    currentAgent,
		label:           label,
		model:           model,
		profiles:        sessionProfiles,
		fragmentsLoaded: fragments,
		commandNames:    commandNames,
		skillNames:      skillNames,
		mcpServerNames:  mcpServerNames,
		runtimeAxis:     runtimeAxis,
		workDir:         workDir,
		aw:              aw,
	})

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
			if merr := EndSession(harp, time.Now()); merr != nil {
				clidiag.Warn("ctxloom", "acp agent: mark session ended: %v", merr)
			}
		}
	})
	// D3: a session gets the child-update push only when
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
		Commands:      sessionCommands,
		InitSummary:   initSummary,
	}, nil
}

// listSessionSkillNames surfaces the cwd's installed Agent Skill names
// (ListSkills — the skill analog of buildSessionCommands' ListCommands, B6a)
// for the session init summary's "skills" line. Unscoped by profile, exactly
// like buildSessionCommands' ListCommands call: both are cwd-wide "what's
// installed" listings, not a per-profile subset — there is no per-session
// skill-selection concept today, so this is honestly labeled "installed",
// never "loaded". nil (degrading the summary line to "none") on a listing
// failure — fault-tolerant like every other assembly step in this opener.
func listSessionSkillNames(ctx context.Context, cfg *config.Config) nameListing {
	res, err := ListSkills(ctx, cfg, ListSkillsRequest{})
	if err != nil {
		clidiag.Warn("ctxloom", "acp agent: listing skills for the session init summary: %v", err)
		return listingFailed(err)
	}
	names := make([]string, 0, len(res.Skills))
	for _, s := range res.Skills {
		names = append(names, s.Name)
	}
	return listed(names)
}

// MCPServerNames extracts a SORTED name list from a resolved MCP server set —
// names only, never command, args or env, any of which can carry a credential.
// Two surfaces read it and need the same answer: the session-init summary's
// "mcp" line (req.MCPServers plus acpSessionMCPServers) and the delegation
// journal/roster. Sorted rather than composition order because the journaled
// value must be stable across runs.
//
// The CONFIGURED set only — what ctxloom asks the engine to attach — never live
// connection status: see buildSessionInitSummary for why that is not observable
// where this summary is built.
func MCPServerNames(servers []agent.ChatMCPServer) []string {
	if len(servers) == 0 {
		return nil
	}
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Name)
	}
	sort.Strings(names)
	return names
}

// buildSessionCommands surfaces the cwd's ctxloom commands (ListCommands/
// GetCommand — the same bundle "commands" surface `ctxloom run --command
// <name>` (internal/cli/run.go) and the MCP commands resource already read)
// as ACP's own agent-role command system (B4, gap G5): an editor's command
// palette gets ctxloom's REAL commands, and a recognized "/<name> ..." in a
// prompt (see acpagent's expandCommand) resolves through the IDENTICAL
// GetCommand path — one command system, two surfaces, never a separate
// reimplementation. nil when no commands are configured for this cwd (the
// session advertises none), degrading fault-tolerantly exactly like
// buildSessionModes/buildSessionLLMs do on an empty listing rather than
// refusing the session open — but a listing FAILURE is returned as an error
// too, so the init summary can tell "no commands" from "could not find out"
// instead of printing the authoritative word "none" for both.
func buildSessionCommands(ctx context.Context, cfg *config.Config) (*SessionCommands, error) {
	res, err := ListCommands(ctx, cfg, ListCommandsRequest{})
	if err != nil {
		clidiag.Warn("ctxloom", "acp agent: listing commands for available_commands_update: %v", err)
		return nil, err
	}
	if len(res.Commands) == 0 {
		return nil, nil
	}
	names := make(map[string]bool, len(res.Commands))
	out := &SessionCommands{Available: make([]CommandInfo, 0, len(res.Commands))}
	for _, c := range res.Commands {
		names[c.Name] = true
		out.Available = append(out.Available, CommandInfo{Name: c.Name, Description: c.Description})
	}
	out.Resolve = func(rctx context.Context, name, rest string) (string, bool, error) {
		if !names[name] {
			return "", false, nil
		}
		got, gerr := GetCommand(rctx, cfg, GetCommandRequest{Name: name})
		if gerr != nil {
			return "", true, gerr
		}
		text := got.Content
		if rest != "" {
			text = text + "\n\n" + rest
		}
		return text, true, nil
	}
	return out, nil
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
	// ComposeChatMCPServers now takes a command override so a
	// runtime:container agent's structured chat can emit the in-container
	// ctxloom path instead of the host's. An ACP session (this flow) has no
	// isolation.Policy in scope to resolve one from — out of scope for the
	// materialize flow's fix; left "" (unchanged behavior) and noted in
	// DECISIONS.md as a residual for the acp/delegation flows to pick up.
	return agent.ComposeChatMCPServers("", cfg.ResolveBundleMCPServers(profiles), existing)
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
			Engine:   s.LLM,
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

// assembleModeContext is assembleModeFunc's seam onto AssembleContext (a
// package var in the style of newACPEngineClient/prepareIsolation) so the
// zero-byte guard below can be driven without authoring a project whose
// assembly legitimately produces nothing.
var assembleModeContext = AssembleContext

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
		res, err := assembleModeContext(ctx, cfg, AssembleContextRequest{Profiles: mode.Profiles})
		if err != nil {
			return "", err
		}
		// A zero-byte assembly is NOT a mode with nothing to say — it is an
		// assembly that produced nothing while reporting success, and handing
		// it back BLANKS the session's lead context while the editor is told
		// the mode switch worked. Refuse it: the session keeps the
		// context it already had, and the editor sees a real failure.
		if strings.TrimSpace(res.Context) == "" {
			return "", fmt.Errorf("mode %q assembled to zero bytes of context — refusing to blank this session's lead context (check the mode's profiles with `ctxloom profile show`)", mode.ID)
		}
		return res.Context, nil
	}
}

// loadConfigForDir loads ctxloom config as seen FROM dir (the ACP session's
// cwd, which need not be this server process's cwd): it walks dir upward for a
// .ctxloom directory and pins config loading to it; absent one, the default
// discovery (process cwd, then home) applies.
//
// config.Load downgrades an unreadable, malformed, schema-invalid or lossily
// migrated config file to kind-tagged Warnings and returns a nil error, so the
// load succeeding says nothing about the config being intact. Every consumer
// must therefore surface those warnings itself — reported to stderr AND
// recorded as fatal-class findings — or a corrupted config.yaml opens a
// session on empty context while the client is told the open succeeded. The
// caller's strictness window turns the recording into a session refusal in
// strict mode; in degraded mode the warnings still print and the session opens.
func loadConfigForDir(dir string) (*config.Config, error) {
	cfg, err := loadConfigForDirRaw(dir)
	if err != nil {
		return nil, err
	}
	config.RecordWarnings(cfg.GetWarnings())
	return cfg, nil
}

// loadConfigForDirRaw is the discovery half: which .ctxloom directory a given
// cwd resolves to, and the load pinned to it.
func loadConfigForDirRaw(dir string) (*config.Config, error) {
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
// oneshot.go package-level seams (prepareIsolation, isolationGateErr).
// Nothing isolation-specific is reinvented below — this is
// that same machinery wired onto the ACP opener, plus one ACP-only posture
// rule layered on top (see acpWorkspaceAxis).

// acpWorkspaceAxis decides the workspace-axis VALUE for one ACP session:
// flagWorkspace (this invocation's --workspace) else the project's
// `workspace:` default — but ONLY when the session is bound to an EXPLICIT
// --agent, never the plain `ctxloom acp server` entry (D-ISO's posture:
// worktree-under-ACP is for deliberately-isolated agent bindings —
// reviewer/executor agents an editor configures as their OWN client entry via
// `ctxloom acp server --agent <name>`, see 'ctxloom acp entries' — never a
// silent default for the entry with no --agent).
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
func acpWorkspaceAxis(cfg *config.Config, flagAgent, currentAgent, flagWorkspace string) (isolation.WorkspaceAxis, error) {
	if flagAgent == "" || currentAgent != flagAgent {
		// The posture rule is deliberate; the SILENCE was not. A user who
		// typed an explicit --workspace and got the shared checkout anyway
		// holds exactly the belief this opener's summary exists to correct —
		// so name the discarded value and what would honor it. The project's
		// `workspace:` default being ignored here is the documented posture
		// rather than a discarded request, so it earns no warning.
		if flagWorkspace != "" {
			clidiag.Warn("ctxloom", "acp agent: --workspace %q is IGNORED for a session with no explicit --agent — this session runs against the shared project checkout, not an isolated worktree (worktree-under-ACP applies only to a deliberately-bound `ctxloom acp server --agent <name>` entry)", flagWorkspace)
		}
		return "", nil
	}
	ws := flagWorkspace
	if ws == "" {
		ws = cfg.GetWorkspace()
	}
	return isolation.ParseWorkspaceAxis(ws)
}

// worktreeIsolationProse is the honesty wording for a session whose workspace
// really is a separate git worktree, carrying one %s for that worktree's path.
// Two surfaces say it — the first-turn announcement and the at-connect session
// init summary — under different lead-ins, and a reader who sees both must be
// told the same thing: what the engine can touch, and what their editor window
// will therefore not show them. One wording, so the two can never drift into
// describing the same posture differently.
const worktreeIsolationProse = "isolated to its own git worktree — %s. " +
	"Your editor's view of this project is NOT touched directly: the engine's edits land in that worktree, " +
	"and this window stays blind to it unless you open the path yourself. " +
	"Results return through ctxloom's normal delegated-child assemble/merge flow."

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
// the package's shared prepareIsolation/isolationGateErr seams: a
// ClassIsolation finding (e.g. a worktree's no-host-credentials-to-seed
// case) refuses the session open
// in strict mode exactly like a fan member would refuse itself — rather
// than silently launching an unseeded/logged-out engine — and the
// already-prepared (but refused) workspace is torn down before returning the
// error. The RUNTIME axis is left at its zero value (isolation.Axes.Runtime
// defaults to host) — ISO1 owns that axis on this same opener. No external
// serialization needed: strictness gives each goroutine's window its own
// findings log.
func prepareACPWorkspace(ctx context.Context, cfg *config.Config, axes isolation.Axes, backendName, agentID, projectDir string, env map[string]string) (*acpWorkspace, error) {
	if !axes.WantsWorktree() {
		return nil, nil
	}
	mark := strictness.Checkpoint()
	policy, ws := prepareIsolation(ctx, axes, backendName, IsolationImageConfig(cfg, backendName), projectDir, agentID, isolation.SessionStateFromEnv(env))
	found := strictness.Since(mark)
	strictness.Close(mark)

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
		aw.announce = fmt.Sprintf("ctxloom: this agent's session is "+worktreeIsolationProse,
			aw.dir)
	}
	return aw, nil
}

// shouldChainFsUpstream decides B5's (gap G14) "one rule" — fs follows the
// engine's authoritative workspace — for ONE session, given its already-
// resolved workspace/runtime axes:
//
//   - aw != nil (ISO2 asked for a worktree — axes.WantsWorktree() gates
//     prepareACPWorkspace's every non-nil return, see its doc): the
//     connected editor's own buffers describe the MAIN checkout, a
//     DIFFERENT tree from wherever this engine actually runs — chaining
//     there would silently serve the WRONG file's content under a
//     right-looking path, this codebase's signature failure mode. This
//     holds even in isolation's rare "wanted a worktree but degraded to
//     None" case (not a git repo, `git worktree add` failed): aw is still
//     non-nil then (prepareACPWorkspace's aw.dir/aw.env are just the
//     harmless projectDir/nil), so this function conservatively still
//     refuses to chain — a missed optimization in that corner, never a
//     wrong-content bug.
//   - agent.IsContainerRuntimeAxis(runtimeAxis) (ISO1 containerized the
//     ENGINE's own subprocess): ISO1's same-path mount already makes
//     local disk correct — this "ctxloom llm serve" subprocess (where
//     internal/acp/session.go's fs handlers actually run) is ALWAYS on
//     the HOST, never inside that container, so req.Path is valid on
//     both sides already; chaining would add nothing.
//   - Otherwise (the fully unisolated default): true — this is the ONLY
//     axis where local disk can diverge from the truth (an editor's
//     unsaved buffer), so it's the only one that chains.
//
// The predicate, not a bare equality against "": runtimeAxis is a parsed
// agent.RuntimeAxis now, and an agent that explicitly declares `runtime:
// host` resolves to the literal RuntimeHost value rather than "" — an
// equality-to-empty check would have wrongly refused to chain for that
// agent even though it is exactly as unisolated as one with no runtime
// declared at all.
func shouldChainFsUpstream(aw *acpWorkspace, runtimeAxis agent.RuntimeAxis) bool {
	return aw == nil && !agent.IsContainerRuntimeAxis(runtimeAxis)
}

// sessionInitSummaryInputs bundles every resolved fact
// buildSessionInitSummary renders. Introduced when the message grew from
// ISO3's single isolation-posture line into ISO4's full SESSION
// INITIALIZATION SUMMARY: the RESOLVED engine/model, composed profiles,
// loaded-fragment set, available commands/skills, the MCP server set
// ctxloom configured, the permission-forwarding posture, and both isolation
// axes with their real paths. Every field is a value ALREADY resolved
// elsewhere in OpenEngineSession — this struct carries them to the pure
// formatting function below without a 15-parameter signature.
type sessionInitSummaryInputs struct {
	cfg            *config.Config
	backendName    string
	requestedAgent string
	currentAgent   string
	label          string
	// model is the value that actually reaches the engine (OpenEngineSession's
	// `model` local, threaded straight onto ChatRequest.Model — see
	// internal/acp/session.go's spawnEnv, which stamps it under the backend's
	// OWN env var, e.g. claude's ANTHROPIC_MODEL, only when both the backend
	// configured one AND req.Model != ""), never the raw config label: a
	// label like "claude-sonnet" names a CONFIG ENTRY, not a model build.
	// Empty means ctxloom pinned nothing and the engine falls back to ITS
	// OWN saved default — said PLAINLY rather than guessed at, because a
	// model cannot self-report its own generation (it is trained before it
	// ships) — ctxloom is the one party that actually knows which value was
	// set, or that none was.
	model    string
	profiles []string
	// fragmentsLoaded, commandNames, skillNames, and mcpServerNames each
	// render through namesOrCount: short lists (nameListCap or fewer) print
	// in full, longer ones collapse to a count plus the CLI command that
	// lists them — the same count-vs-list judgment call applied uniformly,
	// so a project with a large bundle catalog can't turn this summary into
	// a wall of text, while the common small-project case still gets to see
	// real names instead of a bare number.
	fragmentsLoaded nameListing
	commandNames    nameListing
	skillNames      nameListing
	// mcpServerNames is the CONFIGURED set only (what ctxloom asked the
	// engine to attach — req.MCPServers plus acpSessionMCPServers' managed
	// injection) — never live connection status. Status (agent.MCPStatus)
	// rides agent.ChatSessionInfo on the Events channel, populated only once
	// the engine's own session/new handshake completes inside internal/acp's
	// Chat (session.go:129) — which runs in the background relative to
	// OpenEngineSession's synchronous return here (client.Chat above hands
	// back channels before that handshake necessarily finishes). Blocking
	// this summary on the first Events entry to report "connected" would
	// reintroduce the exact turn-gating bug ISO3 fixed for the isolation
	// posture (announceOnFirstEvent); reporting "configured" is the honest,
	// synchronously-knowable fact instead.
	mcpServerNames nameListing
	runtimeAxis    agent.RuntimeAxis
	// workDir is the SAME path OpenEngineSession put on ChatRequest.WorkDir —
	// req.Cwd for every unisolated/container-only session, or aw.dir once
	// ISO2 has isolated the WORKSPACE axis to a worktree.
	workDir string
	aw      *acpWorkspace
}

// nameListCap is namesOrCount's short-list threshold: at or under this many
// entries, print every name (worth reading in full for a typical small
// project); beyond it, collapse to a count so the summary can't balloon on a
// project with a large bundle catalog.
const nameListCap = 8

// namesOrCount renders an assembled name set as either its full contents
// (short lists) or a bare count plus the CLI command that lists it in full
// (long ones) — see sessionInitSummaryInputs' doc for why this applies
// uniformly across fragments/commands/skills/mcp rather than each picking
// its own convention. "none" when the set is empty (a real, distinct fact
// from "some but too many to show").
func namesOrCount(l nameListing, listCmd string) string {
	if l.err != nil {
		return fmt.Sprintf("unavailable — this listing FAILED, so ctxloom cannot say (%v)", l.err)
	}
	names := l.names
	if len(names) == 0 {
		return "none"
	}
	if len(names) <= nameListCap {
		return fmt.Sprintf("%d (%s)", len(names), strings.Join(names, ", "))
	}
	return fmt.Sprintf("%d (see `%s`)", len(names), listCmd)
}

// nameListing is one of the init summary's collapsible name sets together
// with the ONE fact a bare []string cannot carry: whether the listing that
// produced it actually succeeded. A failed listing degrading to nil, rendered
// as the authoritative word "none", is this codebase's signature lie in the
// one artifact whose stated purpose is "what did ctxloom assemble on my
// behalf?" — "I assembled nothing" and "I could not find out" are
// different answers and the editor is entitled to both.
type nameListing struct {
	names []string
	err   error
}

// listed wraps a SUCCESSFUL listing's names (possibly empty — an empty set is
// a real fact).
func listed(names []string) nameListing { return nameListing{names: names} }

// listingFailed marks a listing that could not be performed. err is never nil
// at any call site; a nil err would silently degrade back to "none".
func listingFailed(err error) nameListing {
	if err == nil {
		err = errors.New("listing failed")
	}
	return nameListing{err: err}
}

// sessionPermissionsLine is the init summary's "permissions" fact. It is a
// FIXED string, not a per-session parameter, because OpenEngineSession sets
// ChatRequest.ForwardPermissions: true unconditionally for every ACP session
// regardless of agent/engine/posture (see the `client.Chat` call above) — an
// agent's own declared Permissions/EffectivePermissions (ResolvedAgent) is
// real config but is NOT what this session actually enforces; reporting it
// here would name a posture ctxloom never applies on this path. The one true
// fact, for every posture, is that the connected editor decides every
// approval in real time.
const sessionPermissionsLine = "every tool call is forwarded to your editor for a real-time approval decision (this session never auto-bypasses)"

// sessionIsolationLine renders the init summary's leading fact: the session's
// resolved posture on BOTH isolation axes. It is the summary's one genuinely
// branching value — three postures (neither axis, runtime only, workspace
// only or both) whose wording the rest of the block just embeds — so it is
// composed here rather than inline, where it was the only reason the
// surrounding function had to be read as a whole.
//
// See buildSessionInitSummary's doc for WHY this leads the block, and why the
// container posture renders the mount as an explicit host -> container
// mapping rather than asserting the two paths equal in prose.
func sessionIsolationLine(in sessionInitSummaryInputs) string {
	isolatedWorktree := in.aw != nil && in.aw.announce != ""
	isolatedContainer := agent.IsContainerRuntimeAxis(in.runtimeAxis)
	if !isolatedWorktree && !isolatedContainer {
		return fmt.Sprintf("HOST process (no container); working directory %s (no worktree) — NOT isolated on either axis", in.workDir)
	}

	runtimeDesc := "RUNTIME: a HOST process (no container)"
	if isolatedContainer {
		imgDesc := "an auto-selected image"
		if image := IsolationImageConfig(in.cfg, in.backendName).Image; image != "" {
			imgDesc = "image " + image
		}
		runtimeDesc = fmt.Sprintf("RUNTIME isolated inside a container (%s) — NOT running directly on your host", imgDesc)
	}

	workspaceDesc := fmt.Sprintf("WORKSPACE mounted identically: host %s -> container %s (no worktree)", in.workDir, in.workDir)
	if isolatedWorktree {
		workspaceDesc = fmt.Sprintf("WORKSPACE "+worktreeIsolationProse, in.aw.dir)
	}
	return runtimeDesc + "; " + workspaceDesc
}

// buildSessionInitSummary composes ISO4's at-connect SESSION INITIALIZATION
// SUMMARY: the one artifact answering "what did ctxloom assemble on my
// behalf for this session?" — carried as the single string on
// EngineChat.InitSummary. It is called unconditionally by OpenEngineSession;
// this function alone decides how much to say.
//
// ORDERING is by CONSEQUENCE, not convenience: the agent-not-found WARNING
// (a hard failure mode) returns first and alone; otherwise the block leads
// with isolation (the two axes a wrong belief about is actively dangerous —
// see the `coder`-typo scenario below), then identity (agent/engine/model —
// what is actually answering your prompts), then what ctxloom assembled on
// top of that (profiles/fragments/commands/skills/mcp), then the
// permissions posture. A reader skimming just the first couple of lines
// still learns the facts that can hurt them if wrong.
//
// ALWAYS, not only-when-isolated: this fires on EVERY session, including the
// fully unisolated host+cwd case — the design judgment ISO3 made, kept here,
// is that "you are NOT isolated" is itself the safety-relevant fact (see the
// `coder`-typo scenario in engine_session_iso3_test.go: a user who believes
// they are sandboxed but silently is not cannot infer that belief is wrong
// from anything else in the transcript), and a user cannot infer their own
// posture — the editor that hosts this chat is structurally blind to it, per
// this file's package doc. ISO4 widens what gets said (ctxloom's ENTIRE
// assembled configuration was equally invisible — an editor cannot see
// resolved model/profiles/fragments/commands/skills/MCP status either, and
// MCP status in particular has NO OTHER spec-legal home: G13 established it
// rides `_meta`, which a foreign client may ignore by contract), but keeps
// the same always-fire, single-artifact-per-connect shape: a session opening
// dozens of times a day gets ONE block, not a stream of separate messages
// worth learning to ignore.
//
// The container posture's workspace line renders the host→container mount
// as an explicit MAPPING (both sides shown, not asserted equal in prose):
// ISO1's Invariant 1 (see internal/lm/isolation/runtime.go's identityMapper,
// unconditionally in force — no construction path in this codebase installs
// a non-identity pathMapper today) guarantees isolation.Container's
// containerWorkspace.dir feeds BOTH RunSpec.WorkDir (via toContainer, the
// identity function) and the host-side mount source (ExposeIdentical) from
// that same string — showing "path -> path" lets the reader SEE the
// identity instead of taking prose's word for it, and can never legitimately
// show two different strings while this invariant holds.
//
// The agent-not-found case returns EARLY with its own dedicated message and
// never falls through to the summary below: a failed agent resolution never
// sets currentAgent, so there is no resolved binding to summarize — the
// session runs the bare, unbound profile flow on the host (workDir is still
// known and named, though — the session really is running there).
//
// This now covers ONLY the AUTO-BOUND default agent. An explicit --agent that
// cannot be resolved refuses the session outright (settled: see
// agentBindingError), because substituting a generic session silently drops
// the requested binding's runtime and permissions. A cfg.DefaultAgent naming
// a missing agent still degrades — the editor never asked for that binding —
// and this is what keeps that surviving degrade impossible to miss in the
// chat itself rather than one buried stderr line.
func buildSessionInitSummary(in sessionInitSummaryInputs) string {
	if in.requestedAgent != "" && in.currentAgent == "" {
		return fmt.Sprintf(
			"ctxloom: WARNING — agent %q was requested but NOT FOUND; this session fell "+
				"back to the plain profile flow instead of refusing to open. NONE of that "+
				"agent's engine override, composed profiles, permissions posture, or runtime "+
				"isolation apply — it is running on the HOST, unisolated, against this "+
				"project's live working directory, %s. Check the agent name (see `ctxloom acp "+
				"entries`) and reconnect.",
			in.requestedAgent, in.workDir)
	}

	agentDesc := "no agent bound (profile flow)"
	if in.currentAgent != "" {
		agentDesc = fmt.Sprintf("agent %q (engine %s)", in.currentAgent, in.label)
	}

	profilesDesc := "none"
	if len(in.profiles) > 0 {
		profilesDesc = "[" + strings.Join(in.profiles, ", ") + "]"
	}
	modelDesc := "engine default (ctxloom pinned none)"
	if in.model != "" {
		modelDesc = in.model
	}

	lines := []string{
		"ctxloom: session initialization summary",
		"  isolation : " + sessionIsolationLine(in),
		"  agent     : " + agentDesc,
		"  model     : " + modelDesc,
		"  profiles  : " + profilesDesc,
		"  fragments : " + namesOrCount(in.fragmentsLoaded, "ctxloom run --dry-run") + " loaded into the lead context",
		"  commands  : " + namesOrCount(in.commandNames, "ctxloom command list") + " available",
		"  skills    : " + namesOrCount(in.skillNames, "ctxloom skill list") + " installed",
		"  mcp       : " + namesOrCount(in.mcpServerNames, "ctxloom mcp list") + " configured (connection status is not observable at connect)",
		"  permissions: " + sessionPermissionsLine,
	}
	return strings.Join(lines, "\n")
}

// agentBindingError renders the fatal session-open error for an explicit
// --agent that does not resolve.
//
// This used to be a warning and a degrade to a generic session, which is a
// worse failure than a hard break: an agent binding carries engine, profiles,
// RUNTIME (host vs container) and PERMISSIONS, so the substitute session
// silently drops every one of them. A typo'd `--agent codr` for an agent
// declared `runtime: container` ran on the HOST while the user believed they
// were isolated — the same class as an agent asking for runtime:container
// silently running on the host, except by design. The lone stderr warning is
// buried in a log by most editors; it already cost a 40-minute misdiagnosis
// where an absent agent binding read as "ctxloom emits no thinking".
//
// The counter-argument — an editor config that outlives an agent rename now
// hard-breaks rather than limping — is real, and is why the message lists the
// agents that DO exist and names the flag to change. Limping while dropping
// isolation and permissions is the worse outcome.
func agentBindingError(cfg *config.Config, name string, cause error) error {
	available := make([]string, 0)
	if cfg != nil {
		for _, a := range cfg.LoadAgents() {
			available = append(available, a.Name)
		}
	}
	if len(available) == 0 {
		return fmt.Errorf("agent %q cannot be opened (%w), and no agents are defined in this project — "+
			"define one under agents: in .ctxloom/config.yaml, or omit --agent to open a plain session", name, cause)
	}
	return fmt.Errorf("agent %q cannot be opened (%w); available: %s — "+
		"fix --agent, or omit it to open a plain session. Not degrading to a generic session on purpose: "+
		"an agent binding carries the engine, profiles, runtime (host vs container) and permissions, "+
		"so substituting one would silently drop this session's isolation and permissions",
		name, cause, strings.Join(available, ", "))
}
