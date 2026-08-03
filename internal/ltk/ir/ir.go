// Package ir defines the Command-Graph intermediate representation that every
// shell frontend lowers into. Rules are matched against this IR, so adding a
// new shell never touches the rule engine — it only needs a frontend that can
// produce a *Script.
//
// The IR deliberately does not try to model an entire shell language. It models
// the command graph: a script is a sequence of pipelines; a pipeline is one or
// more simple commands joined by "|"; a simple command is a program with argv,
// env-assignment prefixes, redirects, and any nested scripts it embeds (command
// substitutions, subshells, `bash -c` bodies, ...). That is enough to answer
// "what programs would this command line run, and with what arguments".
package ir

import "slices"

// Shell identifies a shell dialect a frontend can parse.
type Shell string

const (
	ShellSh   Shell = "sh"
	ShellBash Shell = "bash"
	ShellZsh  Shell = "zsh"
	ShellMksh Shell = "mksh"
	ShellPwsh Shell = "pwsh"
	ShellCmd  Shell = "cmd"
)

// knownShells is the closed set of shells the IR recognizes. It is unexported
// and never handed out directly: Valid is the sole validation gate for every
// user-supplied shell name (defaults.shell and each match.shells entry), so an
// importer able to append to or reorder this list could silently redefine what
// the whole process accepts as a shell.
var knownShells = []Shell{ShellSh, ShellBash, ShellZsh, ShellMksh, ShellPwsh, ShellCmd}

// KnownShells returns every shell the IR recognizes, in declaration order. The
// result is a fresh slice each call; writing to it does not change what Valid
// accepts.
func KnownShells() []Shell { return slices.Clone(knownShells) }

// Valid reports whether s is a recognized shell.
func (s Shell) Valid() bool { return slices.Contains(knownShells, s) }

// Connector is the operator that joins a pipeline to the one before it.
type Connector string

const (
	ConnNone Connector = ""   // first pipeline in a script
	ConnSeq  Connector = ";"  // sequential (; or newline)
	ConnAnd  Connector = "&&" // run if previous succeeded
	ConnOr   Connector = "||" // run if previous failed
)

// Script is the lowered form of a whole command string.
type Script struct {
	Shell     Shell
	Pipelines []Pipeline
}

// Pipeline is one or more simple commands joined by "|".
//
// Background/Negated were dropped: frontend/shell was the only
// writer and, past a single test-only reader inside that same package, they
// had no reader anywhere in the repo. Match.matches only ever consulted
// Commands (rules match on Argv), so the two flags never influenced a
// decision — they were pure decoration with a real lowering cost.
type Pipeline struct {
	Connector Connector
	Commands  []SimpleCommand
}

// SimpleCommand is a single program invocation.
//
// Raw was dropped: its doc said "for human-facing
// messages", but no message path ever read it (a deny's Reason comes from
// Rule.Message, rules/eval.go) — rg found zero readers anywhere, tests
// included. It cost a full syntax.Printer pretty-print per command on the
// guard's hot path for a value nothing consumed. Re-add it (with a real
// consumer landing first) if quoting the offending command in a deny message
// is ever built.
type SimpleCommand struct {
	Assignments []Assignment // FOO=bar env prefixes
	Argv        []string     // program + args, best-effort literal resolution
	Redirects   []Redirect
	Nested      []*Script // scripts embedded via $(...), `...`, <(...), ( ... )
}

// Program returns argv[0], or "" if the command has no words.
func (c SimpleCommand) Program() string {
	if len(c.Argv) == 0 {
		return ""
	}
	return c.Argv[0]
}

// Args returns argv[1:].
func (c SimpleCommand) Args() []string {
	if len(c.Argv) <= 1 {
		return nil
	}
	return c.Argv[1:]
}

// Assignment is an environment-variable prefix on a command.
type Assignment struct {
	Name  string
	Value string
}

// Redirect is a single redirection (best-effort; target may be empty if dynamic).
type Redirect struct {
	Op     string
	Target string
}

// Walk visits every SimpleCommand in the script in source (pre-)order,
// descending into nested scripts. fn also receives the *Script that directly
// owns the command being visited — which, for a nested script (a wrapper body,
// a substitution, ...), is NOT necessarily the top-level script Walk was
// called on and may carry a different Shell. A caller that matches against
// "the script's shell" using the top-level Script instead of this owner
// mismatches every rule scoped to a nested dialect (this fixes a real bypass:
// a `shells: [cmd]` rule could never fire on a cmd.exe payload nested inside a
// bash `bash -c "cmd.exe /c …"` wrapper, because the walk had no way to say
// which shell actually owned that command). The visitor returns false to stop
// the walk early; Walk then returns false too. Returns true if the walk
// completed.
func (s *Script) Walk(fn func(owner *Script, c SimpleCommand) bool) bool {
	if s == nil {
		return true
	}
	for _, p := range s.Pipelines {
		for _, c := range p.Commands {
			if !fn(s, c) {
				return false
			}
			for _, ns := range c.Nested {
				if !ns.Walk(fn) {
					return false
				}
			}
		}
	}
	return true
}

// Commands flattens every SimpleCommand reachable from the script, nested
// scripts included, in walk order.
func (s *Script) Commands() []SimpleCommand {
	var out []SimpleCommand
	s.Walk(func(_ *Script, c SimpleCommand) bool {
		out = append(out, c)
		return true
	})
	return out
}
