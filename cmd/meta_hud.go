package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctxloom/ctxloom/internal/config"
)

var metaHudCmd = &cobra.Command{
	Use:   "hud",
	Short: "Output formatted statusline for Claude Code HUD",
	Long: `Reads Claude Code session JSON from stdin and outputs a formatted statusline
combining Claude session info with ctxloom project status.

This command is designed to be used as the statusLine command in Claude Code's
settings.json. It runs after each assistant message and displays context usage,
model info, and active ctxloom profile/bundle counts.

The output uses ANSI escape codes for color in supported terminals.`,
	RunE: runMetaHud,
}

func init() {
	metaCmd.AddCommand(metaHudCmd)
}

// claudeSessionJSON represents the relevant fields from Claude Code's statusline JSON.
type claudeSessionJSON struct {
	Model struct {
		DisplayName string `json:"display_name"`
		ID          string `json:"id"`
	} `json:"model"`
	ContextWindow struct {
		UsedPercentage      float64 `json:"used_percentage"`
		RemainingPercentage float64 `json:"remaining_percentage"`
	} `json:"context_window"`
	Cost struct {
		TotalCostUSD float64 `json:"total_cost_usd"`
	} `json:"cost"`
	Worktree struct {
		Name   string `json:"name"`
		Branch string `json:"branch"`
	} `json:"worktree"`
}

// ctxloomHudInfo holds ctxloom-specific data for the HUD.
type ctxloomHudInfo struct {
	Profile     string
	BundleCount int
}

func runMetaHud(cmd *cobra.Command, args []string) error {
	// Read Claude's JSON from stdin
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		// Fault tolerant: output minimal HUD on read error
		fmt.Print("ctxloom")
		return nil
	}

	var session claudeSessionJSON
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
func formatHud(session claudeSessionJSON, info ctxloomHudInfo) string {
	var parts []string

	// Model name
	model := session.Model.DisplayName
	if model == "" {
		model = session.Model.ID
	}
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
