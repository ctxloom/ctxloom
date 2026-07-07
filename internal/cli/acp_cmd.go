package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/acpagent"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/lm/backends"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

var (
	acpProfile string
	acpLLM     string
	acpAgent   string
)

var acpCmd = &cobra.Command{
	Use:   "acp",
	Short: "Serve ctxloom as an Agent Client Protocol agent (stdio)",
	Long: `Serve ctxloom AS an ACP agent over stdio, so any ACP client (Zed's agent
panel, editor plugins) can drive ctxloom sessions — assembled context,
profiles, and the configured engine — without a bespoke per-editor frontend.

Each session/new opens one engine conversation rooted at the request's cwd
(ctxloom config is discovered from there); ctxloom's assembled context rides
the first turn as a lead block, and client-supplied mcpServers pass through to
the engine. Engine permission requests forward to the connected editor as
session/request_permission — the editor's own approval UI decides. ctxloom
profile sets (the composed defaults, each profile, each agent's composed
set) surface as ACP session modes; session/set_mode re-assembles the lead
context for the next turn, while the ENGINE stays pinned at launch. Sessions
are recorded under ctxloom harp names, and session/load resumes a recorded
harp: its history replays to the client and primes the fresh engine
conversation.

--agent serves one agent as the agent: its composed profiles become the
session context and its engine binding picks the backend (an explicit --llm
still wins). Configure one editor agent entry per agent to pick agents
from the editor — 'ctxloom acp agents' prints the entries ready to paste.

Stdout carries the protocol; all diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return acpagent.Serve(cmd.Context(), os.Stdin, os.Stdout, func(ctx context.Context, req acpagent.OpenRequest) (*acpagent.EngineChat, error) {
			return openACPEngineChat(ctx, req, acpProfile, acpAgent, acpLLM)
		})
	},
}

// acpOpenGateMu serializes each ACP session-open's strictness window
// (Checkpoint → config load/assembly → findingsError). The server opens
// sessions on concurrent goroutines, but strictness Marks only isolate
// SEQUENTIAL windows (see strictness.Mark): unserialized opens interleave,
// and one session's fatal finding would land inside — and refuse — every
// concurrently-opening session. The lock covers assembly only, never the
// engine launch below it, so a slow engine spawn does not block other opens.
var acpOpenGateMu sync.Mutex

// openACPEngineChat is the production ChatOpener: it loads ctxloom config for
// the session's cwd, assembles the context (an agent's composed profiles, or
// the profile flow), resolves the engine label (override → agent engine /
// profile llm → primary), records the session under a harp, and opens the
// plugin's structured chat — the same substrate `run --structured` drives. For
// a resume (session/load) it additionally fetches the recorded harp's history:
// the entries replay to the ACP client, and a rendered transcript primes the
// fresh engine via the first-turn lead block.
func openACPEngineChat(ctx context.Context, req acpagent.OpenRequest, flagProfile, flagAgent, llmOverride string) (*acpagent.EngineChat, error) {
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
	// whole window is serialized (acpOpenGateMu) because concurrent opens would
	// interleave their Mark windows — see the var's doc.
	if gerr := func() error {
		acpOpenGateMu.Lock()
		defer acpOpenGateMu.Unlock()
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
			if rs, rerr := operations.ResolveAgent(ctx, cfg, resolveAgent, llmOverride); rerr != nil {
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
			if ctxResult, cerr := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{Profile: profile}); cerr != nil {
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
		backendName, model = operations.ResolveBackend(cfg, label)

		// An ACP session never runs backend Setup (which writes the managed MCP
		// servers into the engine's settings file), so the managed set rides
		// session/new mcpServers instead. Bundle executables pass the SAME trust
		// gate the run path applies before reaching the engine (fail-closed);
		// client-supplied servers win by name over the managed entries.
		execGate := operations.NewExecutableTrustGate(cfg)
		cfg.SetExecutableTrustGate(execGate.Gate())
		mcpServers = append(append([]agent.ChatMCPServer{}, req.MCPServers...),
			acpSessionMCPServers(cfg, backendName, sessionProfiles, req.MCPServers)...)
		execGate.WarnWithheld()

		// Strict mode: refuse to open the session when config load or assembly
		// recorded a fatal finding, surfacing the full list to the editor. Degraded
		// mode returns nil here and the session opens (ACP's usual fault tolerance).
		return findingsError(startupMark)
	}(); gerr != nil {
		return nil, gerr
	}

	// Session accounting: resume the named harp, or mint a fresh one. A resume
	// of an unknown/unbound harp is a hard error (the client asked for THAT
	// session); a failed mint merely degrades — the session runs unrecorded.
	harp := req.ResumeHarp
	var replay []agent.SessionEntry
	if harp != "" {
		entries, rerr := recordedSessionEntries(ctx, harp)
		if rerr != nil {
			return nil, rerr
		}
		replay = entries
		contextText = joinLeadBlocks(contextText, renderResumedTranscript(harp, replay))
	} else if entry, aerr := operations.AssignSession(req.Cwd, backendName); aerr != nil {
		clidiag.Warn("ctxloom", "acp agent: session accounting unavailable; session will not be resumable: %v", aerr)
	} else {
		harp = entry.HarpName
	}

	env := map[string]string{}
	if harp != "" {
		// The engine (and its SessionStart hooks) see the harp exactly like an
		// interactive run — this is what binds the engine's transcript to the
		// harp so the session becomes loadable later.
		env["CTXLOOM_SESSION_HARP"] = harp
	}

	client, err := pb.DefaultClientFactory()(backendName, label, 0)
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
	})
	if err != nil {
		client.Kill()
		return nil, err
	}

	closeOnce := sync.OnceFunc(func() {
		client.Kill()
		if harp != "" {
			if merr := operations.MarkSessionEnded(harp, time.Now()); merr != nil {
				clidiag.Warn("ctxloom", "acp agent: mark session ended: %v", merr)
			}
		}
	})
	return &acpagent.EngineChat{
		Context:      contextText,
		In:           in,
		Events:       events,
		Errs:         errs,
		Close:        closeOnce,
		Harp:         harp,
		Modes:        buildSessionModes(cfg, profile, defaultProfiles, currentAgent),
		AssembleMode: assembleModeFunc(cfg, label),
		Replay:       replay,
		LLMs:         buildSessionLLMs(cfg, label),
	}, nil
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
func buildSessionLLMs(cfg *config.Config, current string) *acpagent.SessionLLMs {
	names := availableLLMNames(cfg)
	if len(names) == 0 {
		return nil
	}
	llms := &acpagent.SessionLLMs{Current: current}
	for _, n := range names {
		llms.Available = append(llms.Available, acpagent.LLMInfo{ID: n, Name: n})
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
func buildSessionModes(cfg *config.Config, initialProfile string, defaultProfiles []string, currentAgent string) *acpagent.SessionModes {
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
	return sessionModesFrom(profileNames, operations.ListAgents(cfg), initialProfile, defaultProfiles, currentAgent)
}

// sessionModesFrom is the pure mode-list builder behind buildSessionModes:
// default set, profiles, then agents, with the current mode following the
// launch selection (agent > profile > default).
func sessionModesFrom(profileNames []string, subs []operations.AgentEntry, initialProfile string, defaultProfiles []string, currentAgent string) *acpagent.SessionModes {
	if len(profileNames) == 0 && len(subs) == 0 {
		return nil
	}

	defaultName := "default"
	if len(defaultProfiles) > 0 {
		defaultName = "default (" + strings.Join(defaultProfiles, ", ") + ")"
	}
	modes := &acpagent.SessionModes{
		Current:   acpagent.DefaultModeID,
		Available: []acpagent.SessionMode{{ID: acpagent.DefaultModeID, Name: defaultName}},
	}
	seen := false
	for _, name := range profileNames {
		modes.Available = append(modes.Available, acpagent.SessionMode{ID: name, Name: name, Profiles: []string{name}})
		seen = seen || name == initialProfile
	}
	for _, s := range subs {
		modes.Available = append(modes.Available, acpagent.SessionMode{
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
			modes.Available = append(modes.Available, acpagent.SessionMode{ID: initialProfile, Name: initialProfile, Profiles: []string{initialProfile}})
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
func assembleModeFunc(cfg *config.Config, sessionLabel string) func(ctx context.Context, mode acpagent.SessionMode) (string, error) {
	return func(ctx context.Context, mode acpagent.SessionMode) (string, error) {
		if mode.Engine != "" && mode.Engine != sessionLabel {
			clidiag.Warn("ctxloom", "acp agent: mode %q declares engine %q but this session runs %q — the engine is pinned at launch (use `ctxloom acp --agent %s` to honor it)",
				mode.ID, mode.Engine, sessionLabel, strings.TrimPrefix(mode.ID, agentModePrefix))
		}
		res, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{Profiles: mode.Profiles})
		if err != nil {
			return "", err
		}
		return res.Context, nil
	}
}

// recordedSessionEntries resolves a harp to its recorded transcript entries
// via the owning backend's session reader (the plugin reassembles and
// normalizes its own native transcript).
func recordedSessionEntries(ctx context.Context, harp string) ([]agent.SessionEntry, error) {
	entry, err := operations.GetSession(harp)
	if err != nil {
		return nil, fmt.Errorf("unknown session %q: %w", harp, err)
	}
	if entry.SessionID == "" {
		return nil, fmt.Errorf("session %q has no bound transcript to load", harp)
	}
	sess, err := pb.NewSessionReader(entry.Backend, 0).GetSession(ctx, entry.SessionID)
	if err != nil {
		return nil, fmt.Errorf("load session %q: %w", harp, err)
	}
	// Replay primes the resumed engine with the conversation the user had —
	// subagent-interior (sidechain) entries stay out of it.
	return agent.MainThreadEntries(sess.Entries), nil
}

// resumeTranscriptBudget caps how much rendered history primes the resumed
// engine — the TAIL wins (most recent exchange matters most for continuation).
const resumeTranscriptBudget = 32 * 1024

// renderResumedTranscript renders recorded entries as a lead-block section the
// fresh engine can continue from. Only conversation substance is included
// (user/assistant text and tool-call names); thinking, tool output, and system
// entries are bulk without continuation value.
func renderResumedTranscript(harp string, entries []agent.SessionEntry) string {
	var parts []string
	for _, e := range entries {
		switch e.Type {
		case agent.EntryTypeUser:
			if e.Content != "" {
				parts = append(parts, "user: "+e.Content)
			}
		case agent.EntryTypeAssistant:
			if e.Content != "" {
				parts = append(parts, "assistant: "+e.Content)
			}
		case agent.EntryTypeToolUse:
			if e.ToolName != "" {
				parts = append(parts, "assistant ran tool: "+e.ToolName)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}

	start, total := len(parts), 0
	for i := len(parts) - 1; i >= 0; i-- {
		total += len(parts[i]) + 2
		if total > resumeTranscriptBudget {
			break
		}
		start = i
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Resumed session %s\n\nThis conversation continues a previous session; its recorded history follows.\n\n", harp)
	if start > 0 {
		fmt.Fprintf(&b, "(earlier history truncated: %d entries omitted)\n\n", start)
	}
	b.WriteString(strings.Join(parts[start:], "\n\n"))
	return b.String()
}

// joinLeadBlocks joins first-turn lead blocks, dropping empties.
func joinLeadBlocks(blocks ...string) string {
	var nonEmpty []string
	for _, b := range blocks {
		if b != "" {
			nonEmpty = append(nonEmpty, b)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
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

func init() {
	acpCmd.Flags().StringVarP(&acpProfile, "profile", "p", "", "profile to assemble context from (default: the configured defaults)")
	acpCmd.Flags().StringVarP(&acpLLM, "llm", "l", "", "LLM config label to drive (default: the agent's/profile's llm, then the primary)")
	acpCmd.Flags().StringVarP(&acpAgent, "agent", "a", "", "agent to serve as the agent: its composed profiles + engine binding (see 'ctxloom agent list')")
	acpCmd.MarkFlagsMutuallyExclusive("profile", "agent")
	rootCmd.AddCommand(acpCmd)
}
