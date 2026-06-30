package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
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
type agentSessionJSON struct {
	Model         json.RawMessage `json:"model"`
	ContextWindow struct {
		UsedPercentage float64 `json:"used_percentage"`
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

	// Gather ctxloom info (fault tolerant - continue with empty info on error)
	info := gatherCtxloomInfo()

	// Format and output the statusline
	fmt.Print(formatHud(session, info))
	return nil
}

// gatherCtxloomInfo loads ctxloom project info for the HUD.
func gatherCtxloomInfo() ctxloomHudInfo {
	info := ctxloomHudInfo{}

	cfg, err := config.Load()
	if err != nil {
		return info
	}

	// Get active profile
	defaults := cfg.GetDefaultProfiles()
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

	// Context usage with mini bar
	pct := session.ContextWindow.UsedPercentage
	if pct > 0 {
		barColor := contextBarColor(pct)
		bar := contextBar(pct)
		parts = append(parts, fmt.Sprintf("%s%s %.0f%%%s", barColor, bar, pct, colorReset))
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
