// The `ctxloom init` interview's reader and its non-engine questions: reading a
// clean line off the user's own terminal, the personal-repo question, and the
// single dirty-tree question. Engine selection lives in init_engine_select.go.

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// initPrompts handles interactive user prompts during init.
type initPrompts struct {
	reader *bufio.Reader
}

// newInitPromptsFrom builds an initPrompts reading from an arbitrary r
// instead of os.Stdin directly (the production path calls
// newInitPromptsFrom(os.Stdin)).
func newInitPromptsFrom(r io.Reader) *initPrompts {
	p := &initPrompts{reader: bufio.NewReader(r)}

	// If stdin is a terminal, save state and ensure canonical mode
	// This handles cases where parent process left terminal in raw mode
	if term.IsTerminal(int(os.Stdin.Fd())) {
		oldState, err := term.GetState(int(os.Stdin.Fd()))
		if err == nil {
			// Restore to cooked mode by making raw then restoring
			// This is a workaround since there's no "MakeCooked" function
			_, _ = term.MakeRaw(int(os.Stdin.Fd()))
			if rerr := term.Restore(int(os.Stdin.Fd()), oldState); rerr != nil {
				clidiag.Warn("ctxloom", "failed to restore terminal state: %v", rerr)
			}
		}
	}

	return p
}

// readCleanLine reads a line and strips terminal escape sequences and
// non-printing characters. This handles focus events (^[[I, ^[[O), cursor
// movements, etc.
//
// It filters rune-wise, not byte-wise: the values typed at these prompts are
// repo names and filesystem paths, which are legitimately non-ASCII, so
// printable text in any script survives verbatim. A byte that is not valid
// UTF-8 is dropped like any other non-printing byte.
func (p *initPrompts) readCleanLine() (string, error) {
	input, err := p.reader.ReadString('\n')
	if err != nil {
		return "", err
	}

	// Strip CSI escape sequences, keep only printable characters.
	var clean strings.Builder
	for i := 0; i < len(input); {
		if isCSIStart(input, i) {
			i = skipCSISequence(input, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(input[i:])
		if r == utf8.RuneError && size <= 1 {
			i++ // not decodable as UTF-8 — drop the byte
			continue
		}
		if unicode.IsPrint(r) {
			clean.WriteRune(r)
		}
		i += size
	}

	return strings.TrimSpace(clean.String()), nil
}

// isCSIStart reports whether a CSI escape (ESC '[') begins at input[i].
func isCSIStart(input string, i int) bool {
	return input[i] == '\x1b' && i+1 < len(input) && input[i+1] == '['
}

// skipCSISequence returns the index just past the CSI sequence starting at i
// (which points at the ESC). CSI sequences end with a final byte 0x40–0x7e.
func skipCSISequence(input string, i int) int {
	i += 2 // past ESC '['
	for i < len(input) {
		c := input[i]
		i++
		if c >= 0x40 && c <= 0x7e {
			break
		}
	}
	return i
}

// promptPersonalRepos optionally asks for one or more personal ctxloom repos.
// Returns the repos in entry order; an empty slice if the user has none.
// The trust consequence is stated BEFORE entry, not after: adding a repo here
// marks it trusted, and the user must know that while deciding what to type.
func (p *initPrompts) promptPersonalRepos() ([]string, error) {
	fmt.Print("\nDo you have any personal ctxloom repositories? (y/N): ")
	input, err := p.readCleanLine()
	if err != nil {
		return nil, err
	}

	input = strings.ToLower(input)
	if input != "y" && input != "yes" {
		return nil, nil
	}

	fmt.Println("Repos you add here are addresses only — their content takes the review path")
	fmt.Println("('ctxloom review') until you sign your bundles and trust your own signing key.")
	fmt.Println("Enter GitHub repos (e.g., 'myuser/ctxloom-profiles'), one per line. Blank line when done.")
	var repos []string
	for {
		fmt.Printf("  repo %d (blank to finish): ", len(repos)+1)
		repo, err := p.readCleanLine()
		if err != nil {
			return repos, err
		}
		if repo == "" {
			break
		}
		repos = append(repos, repo)
	}

	return repos, nil
}

// dirtyTreeHandlerOption is one menu entry promptDirtyTreeHandler presents:
// value is the literal config value written to dirty_tree_handler, label is
// the full line shown for it (already including its trailing consequence
// text, so the loop below only needs to number and print each one).
type dirtyTreeHandlerOption struct {
	value operations.DirtyTreeHandler
	label string
}

// dirtyTreeHandlerOptions is promptDirtyTreeHandler's menu, in display order.
// Index 0 (DirtyTreeHandlerCommit) is both the built-in default (operations
// package's defaultDirtyTreeHandler) and what a bare Enter picks, mirroring
// promptEngineSelection's "Enter for recommended" convention. The values are
// the operations constants themselves, not copies of their text: this menu
// writes dirty_tree_handler, and the handler that reads it back is the only
// authority on what the four values are. Wording must stay in lockstep with
// the real refusal/warning text a mismatched choice would later hit — see
// internal/operations/delegate.go's
// dirtyTreeFailError/commitDirtyTree/handleDirtyParentTree "stale" branch.
var dirtyTreeHandlerOptions = []dirtyTreeHandlerOption{
	{
		value: operations.DirtyTreeHandlerCommit,
		label: "commit — ctxloom may commit your uncommitted changes onto your current branch on your behalf, so the child can see them (each such commit is announced when it happens) (Recommended)",
	},
	{
		value: operations.DirtyTreeHandlerCopy,
		label: "copy — reproduce your uncommitted changes inside the child's own worktree as uncommitted WIP; nothing on your branch is ever touched",
	},
	{
		value: operations.DirtyTreeHandlerStale,
		label: "stale — the child sees only your last commit; your uncommitted work stays invisible to it",
	},
	{
		value: operations.DirtyTreeHandlerFail,
		label: "fail — refuse the delegation instead of guessing",
	},
}

// promptDirtyTreeHandler asks the interview's ONE dirty-tree question and
// returns both the dirty_tree_handler value and the dirty_tree_commit_ack it
// implies. This is deliberately a single decision, not two: "may ctxloom
// commit your uncommitted work on your behalf" and "what should happen when
// your tree is dirty" are the SAME choice among commit/copy/stale/fail, and
// asking both would just rubber-stamp the first with the second (see this
// task's brief). ack is true if and only if the chosen handler is the commit
// handler (operations.DirtyTreeHandlerCommit) —
// every other handler never mutates the user's repo and needs no
// acknowledgement (config.Config.dirtyTreeCommitAck's doc).
//
// This runs entirely in THIS process, reading raw keystrokes off the user's
// own terminal via readCleanLine — never through an engine session or any
// agent-mediated surface — which is what keeps the resulting ack
// unambiguously human-set, the same property promptPersonalRepos' trust
// consequence relies on.
func (p *initPrompts) promptDirtyTreeHandler() (handler string, ack bool, err error) {
	fmt.Println("\nDelegated agents you launch with agent_run run in an isolated worktree,")
	fmt.Println("which by default can only see your COMMITTED work — nothing you haven't")
	fmt.Println("committed yet. What should happen when your tree has uncommitted changes")
	fmt.Println("at delegation time?")
	fmt.Println()
	for i, opt := range dirtyTreeHandlerOptions {
		fmt.Printf("  %d) %s\n", i+1, opt.label)
	}

	for {
		fmt.Print("\n> (1-4, Enter for recommended): ")
		input, err := p.readCleanLine()
		if err != nil {
			return "", false, err
		}

		var opt dirtyTreeHandlerOption
		if input == "" {
			opt = dirtyTreeHandlerOptions[0]
		} else {
			num, convErr := strconv.Atoi(input)
			if convErr != nil || num < 1 || num > len(dirtyTreeHandlerOptions) {
				fmt.Printf("Please enter a number between 1 and %d, or press Enter for recommended\n", len(dirtyTreeHandlerOptions))
				continue
			}
			opt = dirtyTreeHandlerOptions[num-1]
		}
		return string(opt.value), opt.value == operations.DirtyTreeHandlerCommit, nil
	}
}

// promptForEngineAndRepos runs the interactive engine selection, optional
// personal-repo, and dirty-tree-handler prompts. errNoEngines propagates (the
// prompt already explained it); other prompt failures warn and fall back
// rather than aborting init. A failed dirty-tree prompt falls back to
// ""/false — the same silent-default shape init had before this question
// existed (built-in "commit" default, unacknowledged).
func promptForEngineAndRepos() (engine string, repos []string, dirtyTreeHandler string, dirtyTreeCommitAck bool, err error) {
	prompts := newInitPromptsFrom(os.Stdin)

	engine, err = prompts.promptEngineSelection()
	if err != nil {
		if err == errNoEngines {
			return "", nil, "", false, err
		}
		clidiag.Warn("ctxloom", "failed to read engine selection: %v", err)
		engine = "claude-code"
	}

	repos, repoErr := prompts.promptPersonalRepos()
	if repoErr != nil {
		clidiag.Warn("ctxloom", "failed to read repo selection: %v", repoErr)
		repos = nil
	}

	dirtyTreeHandler, dirtyTreeCommitAck, dtErr := prompts.promptDirtyTreeHandler()
	if dtErr != nil {
		clidiag.Warn("ctxloom", "failed to read dirty-tree handler selection: %v", dtErr)
		dirtyTreeHandler, dirtyTreeCommitAck = "", false
	}

	return engine, repos, dirtyTreeHandler, dirtyTreeCommitAck, nil
}
