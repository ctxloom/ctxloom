// Terminal predicates shared across the CLI. They live here rather than inside
// any one command's file because every consumer is a different command (run,
// pager, item/bundle/mcp show, review, signer): whether stdin/stdout/
// stderr is a terminal decides prompting, pager insertion, and raw-escape
// writes, and none of those decisions belongs to init.

package cli

import (
	"os"

	"golang.org/x/term"
)

// isInteractiveTerminal returns true if both stdin and stdout are terminals.
func isInteractiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

// stdinIsPiped reports whether stdin is NOT a terminal — i.e. content is being
// piped or redirected in. Used to decide when a command should read its task
// from stdin (e.g. `… | ctxloom run --print`).
func stdinIsPiped() bool {
	return !term.IsTerminal(int(os.Stdin.Fd()))
}

// stderrIsTerminal reports whether stderr is a terminal. Checked before
// writing terminal escape sequences to stderr so `2>log` captures don't
// collect raw escape bytes.
func stderrIsTerminal() bool {
	return term.IsTerminal(int(os.Stderr.Fd()))
}

// stdoutIsTerminal reports whether stdout is a terminal. Checked before
// inserting a pager into `session list --full` / `session query --full`
// output so a redirected or piped invocation (`> file`, `| jq`) never gets
// pager control codes mixed in.
func stdoutIsTerminal() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
