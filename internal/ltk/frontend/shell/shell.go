// Package shell implements the Frontend for the POSIX shell family — sh, bash,
// zsh, and mksh — by lowering the mvdan.cc/sh AST into the Command-Graph IR.
//
// Lowering is intentionally lossy: it preserves the command graph (pipelines,
// sequencing, nesting) and the words of each command, but collapses control
// flow. Compound commands (if/for/while/case/functions) are flattened to the
// commands they contain so rules still match programs buried inside them.
//
// Words are expanded with mvdan.cc/sh's expand engine against a best-effort
// environment — the process environment (the hook inherits the callee's env)
// overlaid with assignments seen earlier in the same script. So `t=test; go $t`
// and `$HOME/bin/x` resolve to their real commands; values we can't know
// (command output, positional parameters) expand to empty and are simply not
// matched. Command/process substitutions are not executed: their bodies are
// captured as nested scripts (so rules still see the commands inside) and they
// expand to empty.
package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Frontend lowers POSIX-family shell into IR.
type Frontend struct {
	// env is the base environment for variable resolution. Defaults to the
	// process environment; a zero Frontend resolves no variables.
	env map[string]string
}

// New returns a shell Frontend that resolves variables against the process
// environment (which the hook inherits from the command it is guarding).
func New() *Frontend { return &Frontend{env: envMap(os.Environ())} }

// Shells reports the dialects handled by this frontend.
func (f *Frontend) Shells() []ir.Shell {
	return []ir.Shell{ir.ShellSh, ir.ShellBash, ir.ShellZsh, ir.ShellMksh}
}

func variantFor(sh ir.Shell) syntax.LangVariant {
	switch sh {
	case ir.ShellSh:
		return syntax.LangPOSIX
	case ir.ShellMksh:
		return syntax.LangMirBSDKorn
	case ir.ShellZsh:
		return syntax.LangZsh
	default: // bash and anything unexpected
		return syntax.LangBash
	}
}

// Parse lowers src into a Script. On a parse error it returns a non-nil (empty)
// script alongside the error, so the caller can apply its on_parse_error policy.
//
// Parse never panics. That is a contract, not an aspiration: this frontend is
// the hook's first contact with an arbitrary command line, and a panic here
// BLOCKS a command the operator wrote no rule against — strictly worse than
// missing a rule, and with a stack trace as the agent's only feedback. Neither
// half of the lowering is panic-free by construction: the walk is hand-written
// over an AST shape we do not control, and the expander is third-party
// (mvdan.cc/sh v3.13.1 nil-derefs on an empty parameter name, `${}`, under the
// POSIX variant — see testdata/fuzz/FuzzParse). So a panic is converted into an
// ordinary parse error and the caller's on_parse_error policy decides, exactly
// as it would for malformed syntax. App.Decide holds the outer backstop for
// everything downstream of here.
func (f *Frontend) Parse(_ context.Context, shell ir.Shell, src string) (script *ir.Script, err error) {
	defer func() {
		if r := recover(); r != nil {
			script = &ir.Script{Shell: shell}
			err = fmt.Errorf("internal error lowering %s command: %v", shell, r)
		}
	}()
	parser := syntax.NewParser(syntax.Variant(variantFor(shell)))
	file, err := parser.Parse(strings.NewReader(src), "")
	if err != nil {
		return &ir.Script{Shell: shell}, err
	}
	l := &lowerer{
		shell: shell,
		base:  f.env,
		vars:  map[string]string{},
	}
	return &ir.Script{Shell: shell, Pipelines: l.lowerStmts(file.Stmts)}, nil
}

// lowerer walks the AST, tracking variable assignments for expansion.
type lowerer struct {
	shell ir.Shell
	base  map[string]string // process env (read-only)
	vars  map[string]string // assignments seen so far in this script
}

func (l *lowerer) lowerStmts(stmts []*syntax.Stmt) []ir.Pipeline {
	var out []ir.Pipeline
	for i, st := range stmts {
		conn := ir.ConnSeq
		if i == 0 {
			conn = ir.ConnNone
		}
		out = append(out, l.lowerStmt(st, conn)...)
	}
	return out
}

func (l *lowerer) lowerStmt(st *syntax.Stmt, conn ir.Connector) []ir.Pipeline {
	if st == nil {
		return nil
	}
	return l.lowerCmd(st.Cmd, st, conn)
}

// lowerCmd lowers one statement's command. Every case nil-checks its node: the
// AST is untrusted input as far as this package is concerned, and a nil node
// reaching syntax.Walk panics ("syntax.Walk: unexpected node type <nil>"),
// which would BLOCK the command the hook is guarding — strictly worse than
// missing a rule. A nil Cmd is not hypothetical: a statement that is pure
// redirection (`> file`, `2> err`, `< in`) is valid POSIX with no command word,
// and the parser gives it Cmd == nil.
func (l *lowerer) lowerCmd(cmd syntax.Command, st *syntax.Stmt, conn ir.Connector) []ir.Pipeline {
	switch c := cmd.(type) {
	case nil:
		return l.lowerBareRedirect(st, conn)
	case *syntax.CallExpr:
		if c == nil {
			return l.lowerBareRedirect(st, conn)
		}
		return []ir.Pipeline{{
			Connector: conn,
			Commands:  []ir.SimpleCommand{l.lowerCall(c, st)},
		}}
	case *syntax.BinaryCmd:
		if c == nil {
			return nil
		}
		return l.lowerBinary(c, st, conn)
	case *syntax.Block:
		if c == nil {
			return nil
		}
		return l.lowerGroup(c.Stmts, st, conn)
	case *syntax.Subshell:
		if c == nil {
			return nil
		}
		return l.lowerGroup(c.Stmts, st, conn)
	default:
		return l.lowerCompound(cmd)
	}
}

// lowerBareRedirect lowers a statement with no command word — a bare
// redirection such as `> out.txt` (truncate) or `< in.txt`. It runs no program,
// so it lowers to a command with empty Argv (Program() == "", which no command
// rule can match) that still carries the redirects, keeping the statement
// visible in the IR rather than silently dropping it.
func (l *lowerer) lowerBareRedirect(st *syntax.Stmt, conn ir.Connector) []ir.Pipeline {
	sc := ir.SimpleCommand{}
	if st != nil {
		cfg := l.expandConfig(&sc)
		for _, r := range st.Redirs {
			if r == nil {
				continue
			}
			sc.Redirects = append(sc.Redirects, ir.Redirect{Op: r.Op.String(), Target: l.literal(cfg, r.Word)})
		}
	}
	return []ir.Pipeline{{
		Connector: conn,
		Commands:  []ir.SimpleCommand{sc},
	}}
}

// lowerGroup lowers a `{ … }` block or `( … )` subshell, carrying the
// enclosing statement's connector onto the first pipeline of the group so
// `(cmd) &&` etc. don't silently lose that metadata (lowerStmts alone would
// stamp the first pipeline ConnNone). Matching is unaffected either way — Walk
// only ever looks at Commands. The enclosing *syntax.Stmt is unused now that
// Background/Negated are gone (U072-F03) but kept as a parameter for call-site
// symmetry with the other lower* methods, which all take it.
func (l *lowerer) lowerGroup(stmts []*syntax.Stmt, _ *syntax.Stmt, conn ir.Connector) []ir.Pipeline {
	pipelines := l.lowerStmts(stmts)
	if len(pipelines) > 0 {
		pipelines[0].Connector = conn
	}
	return pipelines
}

// lowerBinary handles "|", "&&", "||" (and any other) binary commands.
func (l *lowerer) lowerBinary(c *syntax.BinaryCmd, st *syntax.Stmt, conn ir.Connector) []ir.Pipeline {
	switch c.Op {
	case syntax.Pipe, syntax.PipeAll:
		return []ir.Pipeline{{
			Connector: conn,
			Commands:  l.flattenPipe(c),
		}}
	case syntax.AndStmt:
		return append(l.lowerStmt(c.X, conn), l.lowerStmt(c.Y, ir.ConnAnd)...)
	case syntax.OrStmt:
		return append(l.lowerStmt(c.X, conn), l.lowerStmt(c.Y, ir.ConnOr)...)
	default:
		return append(l.lowerStmt(c.X, conn), l.lowerStmt(c.Y, ir.ConnSeq)...)
	}
}

// lowerCompound handles a compound command (if/for/while/case/function/...) or a
// construct we don't model: it walks the node to surface every simple command
// inside so rules still match, stopping at each call we capture (lowerCall
// already pulls in any nested substitutions).
// A nil cmd never reaches here (lowerCmd routes it to lowerBareRedirect), but
// the check is kept at the call site of syntax.Walk too: Walk PANICS on a nil
// node, and this is the one place in the package that calls it.
func (l *lowerer) lowerCompound(cmd syntax.Command) []ir.Pipeline {
	if cmd == nil {
		return nil
	}
	var out []ir.Pipeline
	syntax.Walk(cmd, func(n syntax.Node) bool {
		call, ok := n.(*syntax.CallExpr)
		if !ok {
			return true
		}
		out = append(out, ir.Pipeline{
			Connector: ir.ConnSeq,
			Commands:  []ir.SimpleCommand{l.lowerCall(call, nil)},
		})
		return false
	})
	return out
}

// flattenPipe collects every command in a "|"/"|&" chain in order.
func (l *lowerer) flattenPipe(c *syntax.BinaryCmd) []ir.SimpleCommand {
	return append(l.pipeSide(c.X), l.pipeSide(c.Y)...)
}

func (l *lowerer) pipeSide(st *syntax.Stmt) []ir.SimpleCommand {
	if st == nil {
		return nil
	}
	if bc, ok := st.Cmd.(*syntax.BinaryCmd); ok && (bc.Op == syntax.Pipe || bc.Op == syntax.PipeAll) {
		return l.flattenPipe(bc)
	}
	var cmds []ir.SimpleCommand
	for _, p := range l.lowerStmt(st, ir.ConnNone) {
		cmds = append(cmds, p.Commands...)
	}
	return cmds
}

func (l *lowerer) lowerCall(c *syntax.CallExpr, st *syntax.Stmt) ir.SimpleCommand {
	sc := ir.SimpleCommand{}
	cfg := l.expandConfig(&sc)
	for _, a := range c.Assigns {
		name := ""
		if a.Name != nil {
			name = a.Name.Value
		}
		sc.Assignments = append(sc.Assignments, ir.Assignment{Name: name, Value: l.literal(cfg, a.Value)})
	}
	// Expand args as the shell would build argv: field-split, with empty
	// unquoted expansions dropped (so an unset `$X` disappears rather than
	// leaving a stray empty argument).
	if argv, err := expand.Fields(cfg, c.Args...); err == nil {
		sc.Argv = argv
	} else {
		sc.Argv = argvFallback(c.Args)
	}
	if st != nil {
		for _, r := range st.Redirs {
			sc.Redirects = append(sc.Redirects, ir.Redirect{Op: r.Op.String(), Target: l.literal(cfg, r.Word)})
		}
	}
	// A pure assignment (no program word) updates the environment that later
	// commands in this script expand against, e.g. `t=test; go $t`.
	if len(c.Args) == 0 {
		for _, a := range sc.Assignments {
			if a.Name != "" {
				l.vars[a.Name] = a.Value
			}
		}
	}
	return sc
}

// expandConfig builds an expand.Config bound to sc: variables resolve from the
// environment, and command/process substitutions are captured into sc.Nested
// (never executed) and expand to empty.
func (l *lowerer) expandConfig(sc *ir.SimpleCommand) *expand.Config {
	return &expand.Config{
		Env: l.environ(),
		CmdSubst: func(_ io.Writer, cs *syntax.CmdSubst) error {
			l.addNested(sc, cs.Stmts)
			return nil
		},
		ProcSubst: func(ps *syntax.ProcSubst) (string, error) {
			l.addNested(sc, ps.Stmts)
			return "", nil
		},
	}
}

// environ resolves variable names from in-script assignments first, then the
// process environment.
func (l *lowerer) environ() expand.Environ {
	return expand.FuncEnviron(func(name string) string {
		if v, ok := l.vars[name]; ok {
			return v
		}
		return l.base[name]
	})
}

// literal expands a single word (used for assignment values and redirect
// targets, which are not field-split), falling back to the literal text on error.
func (l *lowerer) literal(cfg *expand.Config, w *syntax.Word) string {
	if w == nil {
		return ""
	}
	out, err := expand.Literal(cfg, w)
	if err != nil {
		return literalFallback(w)
	}
	return out
}

func (l *lowerer) addNested(sc *ir.SimpleCommand, stmts []*syntax.Stmt) {
	sc.Nested = append(sc.Nested, &ir.Script{Shell: l.shell, Pipelines: l.lowerStmts(stmts)})
}

// literalFallback concatenates the literal parts of a word, used only when
// expansion errors.
//
// A double-quoted part contributes its own literal parts: `"push"` is one word
// whose text is `push`, exactly as `push` and `'push'` are. Rendering it as ""
// instead does not merely lose quoting — argvFallback drops empty words, so the
// argument disappears from argv and every rule written against it stops
// matching, silently, on a command line the operator did write a rule for.
func literalFallback(w *syntax.Word) string {
	if w == nil {
		return ""
	}
	var b strings.Builder
	writeLiteralParts(&b, w.Parts)
	return b.String()
}

// writeLiteralParts appends the literal text of parts to b, descending into
// double quotes. Parts whose value cannot be known without expanding
// (parameters, substitutions, arithmetic) contribute nothing — the same
// erasure the successful expansion path performs for them.
func writeLiteralParts(b *strings.Builder, parts []syntax.WordPart) {
	for _, part := range parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			if p != nil {
				writeLiteralParts(b, p.Parts)
			}
		}
	}
}

// argvFallback builds argv from literal word text when field expansion errors,
// dropping words that resolve to empty.
func argvFallback(words []*syntax.Word) []string {
	var out []string
	for _, w := range words {
		if s := literalFallback(w); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// envMap turns "K=V" pairs into a map.
func envMap(pairs []string) map[string]string {
	m := make(map[string]string, len(pairs))
	for _, kv := range pairs {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
