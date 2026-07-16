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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
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

// OpenEngineSession is the production ChatOpener body: it loads ctxloom
// config for the session's cwd, assembles the context (an agent's composed
// profiles, or the profile flow), resolves the engine label (override →
// agent engine / profile llm → primary), records the session under a harp,
// and opens the plugin's structured chat — the same substrate `run
// --structured` drives. For a resume (session/load) it additionally fetches
// the recorded harp's history: the entries replay to the ACP client, and a
// rendered transcript primes the fresh engine via the first-turn lead block.
func OpenEngineSession(ctx context.Context, req OpenRequest, acpCoord EngineSessionCoordinator, flagProfile, flagAgent, llmOverride string) (*EngineChat, error) {
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
		runtimeAxis     string
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

	client, err := pb.NewSelfInvokingClientForLabelEnv(backendName, label, 0, spawnEnv)
	if err != nil {
		return nil, err
	}
	in, events, errs, err := client.Chat(ctx, agent.ChatRequest{
		WorkDir: req.Cwd,
		Model:   model,
		Env:     env,
		// The connected editor answers session/request_permission — real
		// interactive approvals in structured mode (no terminal prompt here).
		ForwardPermissions: true,
		MCPServers:         mcpServers,
		Runtime:            runtimeAxis,
	})
	if err != nil {
		client.Kill()
		return nil, err
	}

	closeOnce := sync.OnceFunc(func() {
		client.Kill()
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
