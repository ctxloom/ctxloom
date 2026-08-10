// Engine discovery and selection for `ctxloom init`: which engines are actually
// installed, the numbered menu over them, and the fallbacks that apply when the
// user makes no choice.

package cli

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"strconv"

	"github.com/ctxloom/ctxloom/internal/lm/backends"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// primaryEngines are shown first in the selection menu (curated list).
var primaryEngines = []string{"claude-code", "codex"}

// getAvailableEngines returns engines filtered by what's actually installed.
// Primary engines come first, then secondary engines, all sorted.
func getAvailableEngines() (primary, secondary []string) {
	primarySet := make(map[string]bool)
	for _, e := range primaryEngines {
		primarySet[e] = true
	}

	// Check which primary engines are available
	for _, name := range primaryEngines {
		if backends.IsAvailable(name) {
			primary = append(primary, name)
		}
	}

	// Get secondary engines (all others except mock)
	for _, name := range backends.List() {
		if isMockBackend(name) || primarySet[name] {
			continue
		}
		if backends.IsAvailable(name) {
			secondary = append(secondary, name)
		}
	}

	// Sort secondary for consistent ordering
	sort.Strings(secondary)
	return primary, secondary
}

// errNoEngines is returned when no AI engines are installed.
var errNoEngines = fmt.Errorf("no AI engines installed")

// promptEngineSelection prompts the user to select an AI engine.
// Returns the selected engine name, or the default if only one is available.
func (p *initPrompts) promptEngineSelection() (string, error) {
	primary, secondary := getAvailableEngines()

	switch len(primary) + len(secondary) {
	case 0:
		return "", errNoEngines
	case 1:
		return selectSoleEngine(primary, secondary), nil
	}

	maxOption := printEngineMenu(primary, secondary)
	return p.readEngineChoice(primary, secondary, maxOption)
}

// selectSoleEngine returns (and announces) the only available engine. Exactly
// one of primary/secondary is non-empty here (the sole-engine switch case), so
// the primary list is consulted before indexing secondary — indexing secondary
// unconditionally panics when the only engine is a primary one.
func selectSoleEngine(primary, secondary []string) string {
	var engine string
	if len(primary) > 0 {
		engine = primary[0]
	} else {
		engine = secondary[0]
	}
	fmt.Printf("\nUsing %s (only available engine)\n", engine)
	return engine
}

// printEngineMenu prints the numbered engine menu (with a "more options" entry
// when secondary engines exist) and returns the highest valid option number.
func printEngineMenu(primary, secondary []string) int {
	fmt.Println("\nSelect your AI engine (press Enter for recommended):")
	for i, engine := range primary {
		label := engine
		if i == 0 {
			label += " (Recommended)"
		}
		fmt.Printf("  %d) %s\n", i+1, label)
	}

	maxOption := len(primary)
	if len(secondary) > 0 {
		fmt.Printf("  %d) more options...\n", len(primary)+1)
		maxOption++
	}
	return maxOption
}

// readEngineChoice loops on input until a valid selection is made: empty picks
// the recommended (first primary), a primary number picks that engine, and the
// "more options" entry shows the full list.
func (p *initPrompts) readEngineChoice(primary, secondary []string, maxOption int) (string, error) {
	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			// Enter selects the recommended (first primary). With zero primary
			// engines (only secondaries registered) there is nothing to
			// recommend, so fall through to the full list instead of indexing
			// primary[0] — a latent index-out-of-range panic in init.
			if len(primary) == 0 {
				return p.promptAllEngines(primary, secondary)
			}
			return primary[0], nil
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > maxOption {
			fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", maxOption)
			continue
		}

		if num <= len(primary) {
			return primary[num-1], nil
		}
		return p.promptAllEngines(primary, secondary)
	}
}

// promptAllEngines shows all available engines. The combined list is a fresh
// slice: appending secondary onto primary would write through primary's
// backing array whenever it has spare capacity, corrupting whatever the caller
// holds past its length.
func (p *initPrompts) promptAllEngines(primary, secondary []string) (string, error) {
	allEngines := slices.Concat(primary, secondary)

	fmt.Println("\nAll installed engines:")
	for i, engine := range allEngines {
		fmt.Printf("  %d) %s\n", i+1, engine)
	}

	for {
		fmt.Print("\n> ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", err
		}

		if input == "" {
			continue
		}

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > len(allEngines) {
			fmt.Printf("Please enter a number between 1 and %d\n", len(allEngines))
			continue
		}

		return allEngines[num-1], nil
	}
}

// pickDefaultEngine resolves the engine to use: an explicit selection wins;
// otherwise the first available primary engine; otherwise "claude-code" so init
// never dead-ends without an engine.
func pickDefaultEngine(selected string, primary []string) string {
	if selected != "" {
		return selected
	}
	if len(primary) > 0 {
		return primary[0]
	}
	return "claude-code"
}

// noEnginesInstalled reports whether neither a primary nor a secondary engine
// is available.
func noEnginesInstalled() bool {
	primary, secondary := getAvailableEngines()
	return len(primary) == 0 && len(secondary) == 0
}

// warnNoEnginesDetected prints install guidance to stderr. Init continues with a
// placeholder engine (fault tolerant).
func warnNoEnginesDetected() {
	clidiag.Warn("ctxloom", "no AI engines detected")
	fmt.Fprintln(os.Stderr, "Install one of the following to use ctxloom:")
	fmt.Fprintln(os.Stderr, "  claude-code:  npm install -g @anthropic-ai/claude-code")
	fmt.Fprintln(os.Stderr, "  codex:        npm install -g @openai/codex")
	fmt.Fprintln(os.Stderr, "")
}
