package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/contextmetrics"
)

var hookHudCmd = &cobra.Command{
	Use:    "hud",
	Hidden: true, // Machine callback (statusline command) - not for direct use
	Short:  "Output a formatted statusline for the active agent's HUD",
	Long: `Reads the active agent's session JSON from stdin and outputs a formatted
statusline combining the session's model/context info with ctxloom project status.

This is the command an agent runs for its statusline after each assistant message,
displaying context usage, model info, and active ctxloom profile/bundle counts.

Agent-neutral by design: the input is the common statusline JSON shape (model,
context window, cost, worktree). Claude Code is the only agent today that supports
a command-backed statusline, so it is the only one currently wired to run this;
other agents map onto the same shape as they gain the capability.

The output uses ANSI escape codes for color in supported terminals.`,
	RunE: runHookHud,
}

func init() {
	hookCmd.AddCommand(hookHudCmd)
}

// agentSessionJSON is the common statusline JSON an agent pipes to `hook hud` on
// stdin. Claude Code emits exactly this shape; it is the canonical form other
// agents map onto as they gain command-backed statuslines. Model is captured raw
// so it can be either an object ({display_name|name|id}) or a bare string —
// resolved by modelName.
//
// UsedPercentage is a POINTER because the engine distinguishes two states this
// program must not collapse. Verified against Claude Code 2.1.229 — both its
// shipped payload builder and a live capture — the field is JSON null, with
// current_usage null beside it, for as long as a session has accumulated no
// usage; it becomes an integer 0..100 afterwards. Decoded into a bare float64
// those two states are both 0, and 0 is the single most dangerous value this
// field can take: it reads as "the context is empty" when it means "nobody has
// looked". The pointer keeps "unknown" spellable, and contextSample refuses to
// record a sample without it.
type agentSessionJSON struct {
	SessionID     string          `json:"session_id"`
	Model         json.RawMessage `json:"model"`
	ContextWindow struct {
		UsedPercentage    *float64 `json:"used_percentage"`
		TotalInputTokens  int      `json:"total_input_tokens"`
		ContextWindowSize int      `json:"context_window_size"`
	} `json:"context_window"`
	Cost struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
	Worktree struct {
		Name   string `json:"name"`
		Branch string `json:"branch"`
	} `json:"worktree"`
}

// modelName resolves the display label from the session's model field, which an
// agent may send as an object ({display_name|name|id}) or a bare string.
func (s agentSessionJSON) modelName() string {
	if len(s.Model) == 0 {
		return ""
	}
	var obj struct {
		DisplayName string `json:"display_name"`
		Name        string `json:"name"`
		ID          string `json:"id"`
	}
	if json.Unmarshal(s.Model, &obj) == nil {
		switch {
		case obj.DisplayName != "":
			return obj.DisplayName
		case obj.Name != "":
			return obj.Name
		case obj.ID != "":
			return obj.ID
		}
	}
	var str string
	if json.Unmarshal(s.Model, &str) == nil {
		return str
	}
	return ""
}

// ctxloomHudInfo holds ctxloom-specific data for the HUD.
type ctxloomHudInfo struct {
	Profile     string
	BundleCount int
}

func runHookHud(cmd *cobra.Command, args []string) error {
	// Read Claude's JSON from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		// Fault tolerant: output minimal HUD on read error
		fmt.Print("ctxloom")
		return nil
	}

	var session agentSessionJSON
	if err := json.Unmarshal(input, &session); err != nil {
		// Fault tolerant: output minimal HUD on parse error
		fmt.Print("ctxloom")
		return nil
	}

	// Persist the context-occupancy sample before rendering. The statusline is
	// the only place the engine's own context accounting is handed to ctxloom,
	// so this callback is the sole capture point for the series `context_status`
	// serves back.
	recordContextSample(session)

	// Gather ctxloom info (fault tolerant - continue with empty info on error)
	info := gatherCtxloomInfo()

	// Format and output the statusline
	fmt.Print(formatHud(session, info))
	return nil
}

// contextSample projects one statusline payload onto a metrics sample,
// reporting false when the payload carries nothing worth recording.
//
// Two refusals, both of which would otherwise produce a plausible-looking lie:
// without a harp there is no session directory to write to (and a sample
// attributed to no session is unreadable by the tool that wants it), and
// without a used_percentage the engine has not measured occupancy yet — a
// sample invented for that state would record 0% for a session whose usage is
// simply unknown. Absence of a sample is the honest representation of both.
func contextSample(session agentSessionJSON, harp string, now time.Time) (contextmetrics.Sample, bool) {
	if harp == "" || session.ContextWindow.UsedPercentage == nil {
		return contextmetrics.Sample{}, false
	}
	return contextmetrics.Sample{
		TS:         now,
		Harp:       harp,
		SessionID:  session.SessionID,
		ContextPct: *session.ContextWindow.UsedPercentage,
		TokensUsed: session.ContextWindow.TotalInputTokens,
		Window:     session.ContextWindow.ContextWindowSize,
		Model:      session.modelName(),
	}, true
}

// recordContextSample appends this refresh's sample to the session's series,
// subject to contextmetrics' sampling rule.
//
// Best-effort and silent, matching every other path in this command: a
// statusline that printed a diagnostic would corrupt the status bar, and one
// that printed it on EVERY refresh would do so several times per assistant
// message. That is tolerable here only because the failure does not stay
// hidden from the agent — a series that was never written reads back through
// `context_status` as an explicit "no samples yet", never as 0%.
func recordContextSample(session agentSessionJSON) {
	s, ok := contextSample(session, os.Getenv("CTXLOOM_SESSION_HARP"), time.Now().UTC())
	if !ok {
		return
	}
	_, _ = contextmetrics.Record(s.Harp, s)
}

// gatherCtxloomInfo loads ctxloom project info for the HUD.
func gatherCtxloomInfo() ctxloomHudInfo {
	info := ctxloomHudInfo{}

	cfg, err := config.Load()
	if err != nil {
		return info
	}

	// Get active profile (the default agent's first composed profile)
	defaults := cfg.DefaultAgentProfiles()
	if len(defaults) > 0 {
		info.Profile = defaults[0]

		// Resolve profile to count bundles
		loader := cfg.GetProfileLoader()
		if loader != nil {
			resolved, err := loader.ResolveProfile(info.Profile, nil)
			if err == nil {
				info.BundleCount = len(resolved.Bundles)
			}
		}
	}

	return info
}

// ANSI color helpers
const (
	colorReset  = "\033[0m"
	colorDim    = "\033[2m"
	colorCyan   = "\033[36m"
	colorYellow = "\033[33m"
	colorGreen  = "\033[32m"
	colorRed    = "\033[31m"
)

// contextBarColor returns a color based on context usage percentage.
func contextBarColor(pct float64) string {
	switch {
	case pct >= 80:
		return colorRed
	case pct >= 60:
		return colorYellow
	default:
		return colorGreen
	}
}

// formatHud formats the statusline output.
func formatHud(session agentSessionJSON, info ctxloomHudInfo) string {
	var parts []string

	// Model name
	model := session.modelName()
	if model != "" {
		parts = append(parts, fmt.Sprintf("%s%s%s", colorCyan, model, colorReset))
	}

	// Context usage with mini bar. A null used_percentage (session with no
	// usage yet) renders nothing, exactly as a zero did before it became
	// distinguishable from one.
	if session.ContextWindow.UsedPercentage != nil {
		pct := *session.ContextWindow.UsedPercentage
		if pct > 0 {
			barColor := contextBarColor(pct)
			bar := contextBar(pct)
			parts = append(parts, fmt.Sprintf("%s%s %.0f%%%s", barColor, bar, pct, colorReset))
		}
	}

	// Cost
	if session.Cost.TotalCostUSD > 0 {
		parts = append(parts, fmt.Sprintf("%s$%.2f%s", colorDim, session.Cost.TotalCostUSD, colorReset))
	}

	// ctxloom profile info
	if info.Profile != "" {
		ctxParts := []string{info.Profile}
		if info.BundleCount > 0 {
			ctxParts = append(ctxParts, fmt.Sprintf("%db", info.BundleCount))
		}
		parts = append(parts, fmt.Sprintf("%s%s%s", colorDim, strings.Join(ctxParts, " "), colorReset))
	}

	// Harp session name (Phase 3.5.1). Surfaced here so the user sees
	// the session's identity in the status bar at all times.
	if harp := os.Getenv("CTXLOOM_SESSION_HARP"); harp != "" {
		parts = append(parts, fmt.Sprintf("%s⌁ %s%s", colorDim, harp, colorReset))
	}

	// Worktree indicator
	if session.Worktree.Name != "" {
		branch := session.Worktree.Branch
		if branch == "" {
			branch = session.Worktree.Name
		}
		parts = append(parts, fmt.Sprintf("%s⎇ %s%s", colorDim, branch, colorReset))
	}

	if len(parts) == 0 {
		// The SUCCESS path must never be less informative than the
		// failure paths (runHookHud's read/parse-error branches both print
		// the "ctxloom" sentinel). A sparse session JSON plus no resolvable
		// ctxloom config previously formatted to zero bytes — the statusline
		// vanished and looked like a broken install, indistinguishable from
		// the hook not running at all.
		return "ctxloom"
	}

	return strings.Join(parts, " │ ")
}

// contextBar generates a small progress bar for context usage.
func contextBar(pct float64) string {
	const barWidth = 8
	filled := int(pct / 100 * barWidth)
	if filled > barWidth {
		filled = barWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled)
}
