package cmd

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
)

// profileChoice is the outcome of profile selection: a chosen profile name
// (empty == proceed without a profile), and a quit flag (user aborted the run).
type profileChoice struct {
	Name string
	Quit bool
}

// resolveProfileSelection decides which profile a bare `ctxloom run` should use
// when nothing was selected explicitly. It is only meaningful when no profile,
// fragments, or tags were requested and no default is configured — the caller
// guards that. On a TTY it lists installed profiles and prompts; off a TTY it
// returns the empty choice (proceed without a profile), preserving the
// non-interactive degrade. Listing failures are fault-tolerant: they fall back
// to the empty choice rather than blocking launch (CLAUDE.md).
func resolveProfileSelection(cfg *config.Config) profileChoice {
	res, err := operations.ListProfiles(context.Background(), cfg, operations.ListProfilesRequest{SortBy: "name"})
	if err != nil || len(res.Profiles) == 0 {
		return profileChoice{}
	}
	if !isInteractiveTerminal() {
		return profileChoice{}
	}
	return runProfilePicker(res.Profiles, os.Stdin, os.Stderr)
}

// runProfilePicker is the IoC seam: it renders the menu to out and reads
// selections from in, so the parsing/selection logic is unit-testable without a
// real terminal. A closed/empty input stream resolves to the empty choice
// (proceed without a profile) — the same graceful default as a non-TTY run.
func runProfilePicker(profilesList []operations.ProfileEntry, in io.Reader, out io.Writer) profileChoice {
	renderProfileMenu(profilesList, out)
	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "select profile [1-", len(profilesList), ", Enter/n=none, q=quit]: ")
		if !scanner.Scan() {
			// EOF / no input: degrade to "none" rather than hang or quit.
			fmt.Fprintln(out)
			return profileChoice{}
		}
		switch choice, ok := parseProfileSelection(strings.TrimSpace(scanner.Text()), profilesList); {
		case !ok:
			fmt.Fprintln(out, "ctxloom: invalid selection; enter a row number, 'n' for none, or 'q' to quit")
		default:
			return choice
		}
	}
}

// parseProfileSelection maps one line of input to a choice. ok is false when the
// input is unrecognized (caller re-prompts). Empty input defaults to "none" so
// pressing Enter proceeds without a profile.
func parseProfileSelection(input string, profilesList []operations.ProfileEntry) (profileChoice, bool) {
	switch strings.ToLower(input) {
	case "", "n", "none":
		return profileChoice{}, true
	case "q", "quit":
		return profileChoice{Quit: true}, true
	}
	n, err := strconv.Atoi(input)
	if err != nil || n < 1 || n > len(profilesList) {
		return profileChoice{}, false
	}
	return profileChoice{Name: profilesList[n-1].Name}, true
}

// renderProfileMenu writes the numbered profile list to out.
func renderProfileMenu(profilesList []operations.ProfileEntry, out io.Writer) {
	fmt.Fprintln(out, "ctxloom: no default profile configured — pick one for this run:")
	for i, p := range profilesList {
		if p.Description != "" {
			fmt.Fprintf(out, "  [%d] %s — %s\n", i+1, p.Name, p.Description)
		} else {
			fmt.Fprintf(out, "  [%d] %s\n", i+1, p.Name)
		}
	}
	fmt.Fprintln(out, "  [n] none (run without a profile)")
	fmt.Fprintln(out, "  [q] quit")
}
