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
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

var (
	acpProfile string
	acpLLM     string
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
profiles surface as ACP session modes (session/set_mode re-assembles the lead
context for the next turn). Sessions are recorded under ctxloom harp names,
and session/load resumes a recorded harp: its history replays to the client
and primes the fresh engine conversation.

Stdout carries the protocol; all diagnostics go to stderr.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return acpagent.Serve(cmd.Context(), os.Stdin, os.Stdout, func(ctx context.Context, req acpagent.OpenRequest) (*acpagent.EngineChat, error) {
			return openACPEngineChat(ctx, req, acpProfile, acpLLM)
		})
	},
}

// openACPEngineChat is the production ChatOpener: it loads ctxloom config for
// the session's cwd, assembles the profile's context, resolves the engine
// label (override → profile llm → primary), records the session under a harp,
// and opens the plugin's structured chat — the same substrate `run
// --structured` drives. For a resume (session/load) it additionally fetches
// the recorded harp's history: the entries replay to the ACP client, and a
// rendered transcript primes the fresh engine via the first-turn lead block.
func openACPEngineChat(ctx context.Context, req acpagent.OpenRequest, flagProfile, llmOverride string) (*acpagent.EngineChat, error) {
	cfg, err := loadConfigForDir(req.Cwd)
	if err != nil {
		return nil, err
	}

	profile := req.Profile
	if profile == "" {
		profile = flagProfile
	}

	// Context assembly is fault-tolerant (CLAUDE.md): a failed assembly warns
	// and the session still opens — the engine runs without ctxloom context
	// rather than refusing the editor's session.
	var contextText, profileLLM string
	var resolvedProfiles []string
	if ctxResult, cerr := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{Profile: profile}); cerr != nil {
		clidiag.Warn("ctxloom", "acp agent: context assembly failed; continuing without context: %v", cerr)
	} else {
		contextText = ctxResult.Context
		profileLLM = ctxResult.ProfileLLM
		resolvedProfiles = ctxResult.Profiles
	}

	label := llmOverride
	if label == "" {
		label = profileLLM
	}
	if label == "" {
		label = cfg.PrimaryLabel()
	}
	backendName, model := operations.ResolveBackend(cfg, label)

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
		MCPServers:         req.MCPServers,
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
		Context:         contextText,
		In:              in,
		Events:          events,
		Errs:            errs,
		Close:           closeOnce,
		Harp:            harp,
		Modes:           buildSessionModes(cfg, profile, resolvedProfiles),
		AssembleProfile: assembleProfileFunc(cfg),
		Replay:          replay,
	}, nil
}

// buildSessionModes surfaces the cwd's ctxloom profiles as ACP session modes:
// a synthetic "default" mode for the configured default set (which may compose
// several profiles), then one mode per installed profile. nil when the profile
// list is unavailable — the session simply advertises no modes.
func buildSessionModes(cfg *config.Config, initialProfile string, resolvedProfiles []string) *acpagent.SessionModes {
	loader := cfg.GetProfileLoader()
	if loader == nil {
		return nil
	}
	list, err := loader.List()
	if err != nil {
		clidiag.Warn("ctxloom", "acp agent: listing profiles for session modes: %v", err)
		return nil
	}
	if len(list) == 0 {
		return nil
	}

	defaultName := "default"
	if len(resolvedProfiles) > 0 {
		defaultName = "default (" + strings.Join(resolvedProfiles, ", ") + ")"
	}
	modes := &acpagent.SessionModes{
		Current:   acpagent.DefaultModeID,
		Available: []acpagent.SessionMode{{ID: acpagent.DefaultModeID, Name: defaultName}},
	}
	seen := false
	for _, p := range list {
		modes.Available = append(modes.Available, acpagent.SessionMode{ID: p.Name, Name: p.Name})
		seen = seen || p.Name == initialProfile
	}
	if initialProfile != "" {
		if !seen {
			modes.Available = append(modes.Available, acpagent.SessionMode{ID: initialProfile, Name: initialProfile})
		}
		modes.Current = initialProfile
	}
	return modes
}

// assembleProfileFunc backs session/set_mode: re-assemble the lead context for
// the chosen profile ("" = the configured defaults).
func assembleProfileFunc(cfg *config.Config) func(ctx context.Context, profile string) (string, error) {
	return func(ctx context.Context, profile string) (string, error) {
		res, err := operations.AssembleContext(ctx, cfg, operations.AssembleContextRequest{Profile: profile})
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
	return sess.Entries, nil
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
	acpCmd.Flags().StringVarP(&acpLLM, "llm", "l", "", "LLM config label to drive (default: the profile's llm, then the primary)")
	rootCmd.AddCommand(acpCmd)
}
