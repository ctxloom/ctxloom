// The `ctxloom init` interview's reader and its non-engine questions: reading a
// clean line off the user's own terminal, the personal-repo question, and the
// defaults init takes instead of asking. Engine selection lives in
// init_engine_select.go.

package cli

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/launch"
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

// initDefaultHeadlessPosture is the seed agent's headless posture an
// interactive init writes: claude's acceptEdits, which lets file edits through
// without asking. A headless run has no one to answer a prompt, so anything
// else the posture would ask about is denied. Interactive init only offers
// real engines, and claude is the one whose vocabulary carries this mode.
const initDefaultHeadlessPosture = "acceptEdits"

// initDefaultDirtyTreeLine and initDefaultHeadlessLine are what an interactive
// init prints for the two answers it takes without asking: what it chose and
// how to change it.
const (
	initDefaultDirtyTreeLine = "Delegation from a dirty tree: ctxloom commits your uncommitted work for the child only " +
		"after you run `ctxloom manage commit trust`; until then such a delegation stops and says so. " +
		"Choose copy, stale or fail instead with dirty_tree_handler in `ctxloom config edit`."
	initDefaultHeadlessLine = "Headless runs (one-shot and delegated) use the " + initDefaultHeadlessPosture +
		" posture: file edits go through, anything else that would ask is denied. " +
		"Change it: ctxloom agent edit default --permissions <posture>"
)

// takeInterviewDefaults answers the two advanced questions init no longer
// asks: the commit dirty-tree handler and the acceptEdits headless posture. It does
// not grant the commit acknowledgement. That consent is only ever a human act
// (config.DirtyTreeCommitAcknowledged), and the delegation that first needs
// it refuses and names `ctxloom manage commit trust`.
func takeInterviewDefaults(out io.Writer) (dirtyTreeHandler, headlessPermissions string) {
	_, _ = fmt.Fprintln(out, initDefaultDirtyTreeLine)
	_, _ = fmt.Fprintln(out, initDefaultHeadlessLine)
	return string(launch.DirtyTreeHandlerCommit), initDefaultHeadlessPosture
}

// promptForEngineAndRepos runs the interactive engine selection and the
// optional personal-repo prompt. errNoEngines propagates (the prompt already
// explained it); other prompt failures warn and fall back rather than
// aborting init.
func promptForEngineAndRepos() (engineName string, repos []string, err error) {
	prompts := newInitPromptsFrom(os.Stdin)

	engineName, err = prompts.promptEngineSelection()
	if err != nil {
		if err == errNoEngines {
			return "", nil, err
		}
		clidiag.Warn("ctxloom", "failed to read engine selection: %v", err)
		engineName = operations.DefaultEngineName(App().Engines())
	}

	repos, repoErr := prompts.promptPersonalRepos()
	if repoErr != nil {
		clidiag.Warn("ctxloom", "failed to read repo selection: %v", repoErr)
		repos = nil
	}
	return engineName, repos, nil
}
