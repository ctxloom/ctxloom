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

// KnownShells lists every shell the IR recognizes.
var KnownShells = []Shell{ShellSh, ShellBash, ShellZsh, ShellMksh, ShellPwsh, ShellCmd}

// Valid reports whether s is a recognized shell.
func (s Shell) Valid() bool { return slices.Contains(KnownShells, s) }

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
type Pipeline struct {
	Connector  Connector
	Background bool // terminated with &
	Negated    bool // prefixed with !
	Commands   []SimpleCommand
}

// SimpleCommand is a single program invocation.
type SimpleCommand struct {
	Assignments []Assignment // FOO=bar env prefixes
	Argv        []string     // program + args, best-effort literal resolution
	Redirects   []Redirect
	Nested      []*Script // scripts embedded via $(...), `...`, <(...), ( ... )
	Raw         string    // original rendered text, for human-facing messages
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
// descending into nested scripts. The visitor returns false to stop the walk
// early; Walk then returns false too. Returns true if the walk completed.
func (s *Script) Walk(fn func(SimpleCommand) bool) bool {
	if s == nil {
		return true
	}
	for _, p := range s.Pipelines {
		for _, c := range p.Commands {
			if !fn(c) {
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
	s.Walk(func(c SimpleCommand) bool {
		out = append(out, c)
		return true
	})
	return out
}
