// Package cmd implements the Windows cmd.exe (batch) frontend. No AST parser
// exists for cmd, so this is a small hand-written lexer + parser covering the
// command grammar that matters for rule matching:
//
//   - operators: & (sequence), && (and), || (or), | (pipe)
//   - grouping: ( … ) (flattened — its commands are surfaced for matching)
//   - quoting: "…" (one token; quotes stripped)
//   - escaping: ^ (the next character is literal, incl. & | ( ) etc.)
//   - variables: %VAR% is parsed as part of the word (not expanded — cmd value
//     resolution is out of scope; see shell for variable resolution)
//   - redirections: >, >>, <, 2>, 2>&1 (consumed, kept off argv)
//
// Control-flow keywords (for/if/do/else) are surfaced as ordinary command words
// rather than interpreted; this is sufficient for matching real programs and is
// documented as a limitation.
package cmd

import (
	"context"
	"fmt"
	"strings"

	"github.com/ctxloom/ctxloom/internal/ltk/ir"
)

// Frontend lowers cmd.exe command lines into the IR.
type Frontend struct{}

// New returns a cmd Frontend.
func New() *Frontend { return &Frontend{} }

// Shells reports the dialects handled by this frontend.
func (f *Frontend) Shells() []ir.Shell { return []ir.Shell{ir.ShellCmd} }

// Parse lowers src into a Script. It is best-effort: whatever it recovers is
// always returned, but it now DOES report an error when tokens are
// left over after a full parseSequence — the only way that happens is an
// unmatched ')' at the top level (parseSequence stops there so a group's
// caller, parsePipeline, can consume it; at the true top there is no such
// caller). Previously this silently dropped every token after the stray ')'
// with no signal at all: `echo hi) & del /f /q important-file`
// parsed as just `echo hi`, so a deny rule targeting `del` never even saw it,
// while real cmd.exe still runs everything after the ')' verbatim (it has no
// special meaning there). Reporting it as a parse error routes it through the
// same on_parse_error policy every other frontend's failure already does,
// instead of a bespoke silent truncation.
func (f *Frontend) Parse(_ context.Context, shell ir.Shell, src string) (*ir.Script, error) {
	l := &lexer{s: src}
	p := &parser{toks: l.run()}
	pipelines := p.parseSequence()
	script := &ir.Script{Shell: shell, Pipelines: pipelines}
	if p.pos < len(p.toks) {
		return script, fmt.Errorf("unexpected %q at top level (unmatched ')' or similar)", p.toks[p.pos].text)
	}
	return script, nil
}

// --- lexer ---

type tkind uint8

const (
	tWord tkind = iota
	tAnd        // &&
	tOr         // ||
	tSeq        // & or newline
	tPipe       // |
	tLParen
	tRParen
	tRedir // text holds the operator
)

type tok struct {
	kind tkind
	text string
}

type lexer struct {
	s       string
	i       int
	toks    []tok
	buf     strings.Builder
	hasWord bool
}

func (l *lexer) flush() {
	if l.hasWord {
		l.toks = append(l.toks, tok{kind: tWord, text: l.buf.String()})
		l.buf.Reset()
		l.hasWord = false
	}
}

func (l *lexer) add(b byte) { l.buf.WriteByte(b); l.hasWord = true }

// emit appends a non-word token, flushing any pending word first so it keeps
// its place in the stream. Flushing here rather than at each call site is what
// makes the ordering unstateable-wrongly: every operator ends the word before
// it, and a caller that forgets loses that word into the next one — dropping
// one such flush merges `X=go` with the `&` that follows it, and only a single
// test in this package notices.
func (l *lexer) emit(k tkind, t string) {
	l.flush()
	l.toks = append(l.toks, tok{kind: k, text: t})
}

func (l *lexer) run() []tok {
	for l.i < len(l.s) {
		switch l.s[l.i] {
		case '^':
			l.lexCaret()
		case '"':
			l.lexQuoted()
		case ' ', '\t', '\r':
			l.flush()
			l.i++
		case '\n':
			l.emit(tSeq, "\n")
			l.i++
		case '&':
			l.lexPair('&', tAnd, "&&", tSeq, "&")
		case '|':
			l.lexPair('|', tOr, "||", tPipe, "|")
		case '(':
			l.emit(tLParen, "(")
			l.i++
		case ')':
			l.emit(tRParen, ")")
			l.i++
		case '>', '<':
			l.lexRedir()
		case '%':
			if !l.lexPercent() {
				l.add(l.s[l.i])
				l.i++
			}
		default:
			l.add(l.s[l.i])
			l.i++
		}
	}
	l.flush()
	return l.toks
}

// lexCaret handles ^: the next character is taken literally, except at end of
// line, where cmd reads `^` as a LINE CONTINUATION — the caret and the line
// break both disappear and the word carries on. Escaping the newline into the
// word instead makes `git^<nl> push` lower to argv[0] "git\n", which no rule
// targeting `git push` matches even though cmd.exe joins the lines and runs
// it. A `^` with nothing after it has nothing to escape and produces nothing.
func (l *lexer) lexCaret() {
	switch {
	case l.i+1 >= len(l.s):
		l.i++
	case l.s[l.i+1] == '\n':
		l.i += 2
	case l.s[l.i+1] == '\r' && l.i+2 < len(l.s) && l.s[l.i+2] == '\n':
		l.i += 3
	default:
		l.add(l.s[l.i+1])
		l.i += 2
	}
}

// lexQuoted reads a "…" segment (quotes stripped) until the next quote or end.
func (l *lexer) lexQuoted() {
	l.hasWord = true
	l.i++
	for l.i < len(l.s) && l.s[l.i] != '"' {
		if l.s[l.i] == '%' && l.lexPercent() {
			continue
		}
		l.buf.WriteByte(l.s[l.i])
		l.i++
	}
	if l.i < len(l.s) {
		l.i++ // closing quote
	}
}

// lexPercent consumes a %VAR% run into the current word (kept literally; cmd
// value resolution is out of scope). Returns false if there is no closing %.
func (l *lexer) lexPercent() bool {
	j := strings.IndexByte(l.s[l.i+1:], '%')
	if j < 0 {
		return false
	}
	l.buf.WriteString(l.s[l.i : l.i+j+2])
	l.hasWord = true
	l.i += j + 2
	return true
}

// lexPair emits a doubled operator (e.g. &&) or its single form (&).
func (l *lexer) lexPair(ch byte, dbl tkind, dblText string, single tkind, singleText string) {
	if l.i+1 < len(l.s) && l.s[l.i+1] == ch {
		l.emit(dbl, dblText)
		l.i += 2
	} else {
		l.emit(single, singleText)
		l.i++
	}
}

// lexRedir reads a redirection operator, dropping any file-descriptor prefix
// (e.g. the 2 in 2>) so it does not leak into argv.
func (l *lexer) lexRedir() {
	l.dropFDPrefix()
	c := l.s[l.i]
	o := string(c)
	l.i++
	if c == '>' && l.i < len(l.s) && l.s[l.i] == '>' {
		o += ">"
		l.i++
	}
	if l.i < len(l.s) && l.s[l.i] == '&' {
		o += "&"
		l.i++
	}
	l.emit(tRedir, o)
}

// dropFDPrefix removes the file-descriptor digit cmd reads immediately before
// a redirection operator. It is exactly ONE digit: `foo 123>out` runs foo with
// the argument `12` and redirects handle 3, and `echo abc1>out` echoes `abc`.
// Taking the whole word instead lost a real argument from argv whenever it
// happened to be all digits, and left a handle digit in argv whenever it did
// not.
func (l *lexer) dropFDPrefix() {
	if !l.hasWord {
		return
	}
	w := l.buf.String()
	if w == "" || w[len(w)-1] < '0' || w[len(w)-1] > '9' {
		return
	}
	l.buf.Reset()
	if rest := w[:len(w)-1]; rest != "" {
		l.buf.WriteString(rest)
	} else {
		l.hasWord = false
	}
}

// --- parser ---

type parser struct {
	toks []tok
	pos  int
}

func (p *parser) peek() (tok, bool) {
	if p.pos < len(p.toks) {
		return p.toks[p.pos], true
	}
	return tok{}, false
}

// parseSequence parses statements separated by & && || newline, until EOF or a
// ')'. The ')' is left for the caller (parsePipeline) to consume.
func (p *parser) parseSequence() []ir.Pipeline {
	var out []ir.Pipeline
	conn := ir.ConnNone
	for {
		t, ok := p.peek()
		if !ok || t.kind == tRParen {
			break
		}
		out = append(out, p.parsePipeline(conn)...)
		t, ok = p.peek()
		if !ok || t.kind == tRParen {
			break
		}
		switch t.kind {
		case tAnd:
			conn = ir.ConnAnd
		case tOr:
			conn = ir.ConnOr
		default:
			conn = ir.ConnSeq
		}
		p.pos++ // consume the separator
	}
	return out
}

// parsePipeline parses one pipeline (commands joined by |) or a ( … ) group,
// which is flattened into its inner pipelines. The connector that joined the
// GROUP to the preceding statement belongs to the first pipeline the group
// contributes — parseSequence starts a fresh sequence and stamps that one
// ConnNone — so it is re-applied here. The shell frontend's lowerGroup does
// the same; without it `echo a && (…)` loses its `&&` from the IR entirely and
// the group reads as unconditional.
func (p *parser) parsePipeline(conn ir.Connector) []ir.Pipeline {
	if t, ok := p.peek(); ok && t.kind == tLParen {
		p.pos++ // (
		inner := p.parseSequence()
		if t2, ok := p.peek(); ok && t2.kind == tRParen {
			p.pos++
		}
		p.skipRedirs() // trailing redirs on the group, e.g. (...) > file
		if len(inner) > 0 {
			inner[0].Connector = conn
		}
		return inner
	}

	var cmds []ir.SimpleCommand
	for {
		if sc, ok := p.parseSimpleCommand(); ok {
			cmds = append(cmds, sc)
		}
		if t, ok := p.peek(); ok && t.kind == tPipe {
			p.pos++
			continue
		}
		break
	}
	if len(cmds) == 0 {
		return nil
	}
	return []ir.Pipeline{{Connector: conn, Commands: cmds}}
}

func (p *parser) parseSimpleCommand() (ir.SimpleCommand, bool) {
	var sc ir.SimpleCommand
	got := false
	for {
		t, ok := p.peek()
		if !ok {
			break
		}
		switch t.kind {
		case tWord:
			sc.Argv = append(sc.Argv, t.text)
			got = true
			p.pos++
		case tRedir:
			p.pos++
			target := ""
			if w, ok := p.peek(); ok && w.kind == tWord {
				target = w.text
				p.pos++
			}
			sc.Redirects = append(sc.Redirects, ir.Redirect{Op: t.text, Target: target})
		default:
			return sc, got
		}
	}
	return sc, got
}

// skipRedirs consumes redirection operators (and their targets) with no command.
func (p *parser) skipRedirs() {
	for {
		t, ok := p.peek()
		if !ok || t.kind != tRedir {
			return
		}
		p.pos++
		if w, ok := p.peek(); ok && w.kind == tWord {
			p.pos++ // target
		}
	}
}
